package managementasset

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

const (
	defaultManagementReleaseURL  = "https://api.github.com/repos/zuohuadong/codeProxy/releases/latest"
	defaultManagementFallbackURL = "https://cpamc.router-for.me/"
	managementAssetName          = "management.html"
	panelDistAssetName           = "panel-dist.zip"
	panelEntryName               = "manage.html"
	panelDigestMarkerName        = ".panel-dist.sha256"
	localPanelMarkerName         = ".local-panel-sha256"
	httpUserAgent                = "CLIProxyAPI-management-syncer"
	managementSyncMinInterval    = 30 * time.Second
	updateCheckInterval          = 3 * time.Hour
	maxAssetDownloadSize         = 50 << 20  // 50 MB safety limit for management asset downloads
	maxExtractedPanelSize        = 200 << 20 // 200 MB safety limit for expanded panel assets
)

// ManagementFileName exposes the control panel asset filename.
const ManagementFileName = managementAssetName

var (
	lastUpdateCheckMu   sync.Mutex
	lastUpdateCheckTime time.Time
	currentConfigPtr    atomic.Pointer[config.Config]
	schedulerOnce       sync.Once
	schedulerConfigPath atomic.Value
	sfGroup             singleflight.Group
)

// SetCurrentConfig stores the latest configuration snapshot for management asset decisions.
func SetCurrentConfig(cfg *config.Config) {
	if cfg == nil {
		currentConfigPtr.Store(nil)
		return
	}
	currentConfigPtr.Store(cfg)
}

// StartPanelAssetSyncer launches a background goroutine that periodically ensures the management asset is up to date.
// It respects the disable-control-panel flag on every iteration and supports hot-reloaded configurations.
func StartPanelAssetSyncer(ctx context.Context, configFilePath string) {
	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		log.Debug("management asset syncer skipped: empty config path")
		return
	}

	schedulerConfigPath.Store(configFilePath)

	schedulerOnce.Do(func() {
		go runPanelAssetSyncer(ctx)
	})
}

func runPanelAssetSyncer(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	ticker := time.NewTicker(updateCheckInterval)
	defer ticker.Stop()

	runOnce := func() {
		cfg := currentConfigPtr.Load()
		if reason, skip := autoUpdateSkipReason(cfg); skip {
			log.Debugf("management asset auto-updater skipped: %s", reason)
			return
		}

		configPath, _ := schedulerConfigPath.Load().(string)
		staticDir := StaticDir(configPath)
		EnsureLatestManagementHTML(ctx, staticDir, cfg.ProxyURL, cfg.RemoteManagement.PanelGitHubRepository)
	}

	runOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

func autoUpdateSkipReason(cfg *config.Config) (string, bool) {
	if cfg == nil {
		return "config not yet available", true
	}
	if cfg.Home.Enabled {
		return "cluster mode enabled", true
	}
	if cfg.RemoteManagement.DisableControlPanel {
		return "control panel disabled", true
	}
	if cfg.RemoteManagement.DisableAutoUpdatePanel {
		return "disable-auto-update-panel is enabled", true
	}
	return "", false
}

func newHTTPClient(proxyURL string) *http.Client {
	client := &http.Client{Timeout: 15 * time.Second}

	sdkCfg := &sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(proxyURL)}
	util.SetProxy(sdkCfg, client)

	return client
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type releaseResponse struct {
	Assets []releaseAsset `json:"assets"`
}

// StaticDir resolves the directory that stores the management control panel asset.
func StaticDir(configFilePath string) string {
	if override := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")); override != "" {
		cleaned := filepath.Clean(override)
		if isPanelEntryFile(cleaned) {
			return filepath.Dir(cleaned)
		}
		return cleaned
	}

	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, "static")
	}

	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return ""
	}

	base := filepath.Dir(configFilePath)
	fileInfo, err := os.Stat(configFilePath)
	if err == nil {
		if fileInfo.IsDir() {
			base = configFilePath
		}
	}

	return filepath.Join(base, "static")
}

// FilePath resolves the absolute path to the management control panel asset.
func FilePath(configFilePath string) string {
	if override := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")); override != "" {
		cleaned := filepath.Clean(override)
		if isPanelEntryFile(cleaned) {
			return cleaned
		}
		return panelEntryPath(cleaned)
	}

	dir := StaticDir(configFilePath)
	if dir == "" {
		return ""
	}
	return panelEntryPath(dir)
}

func isPanelEntryFile(path string) bool {
	base := filepath.Base(filepath.Clean(path))
	return strings.EqualFold(base, managementAssetName) || strings.EqualFold(base, panelEntryName)
}

func panelEntryPath(staticDir string) string {
	staticDir = strings.TrimSpace(staticDir)
	if staticDir == "" {
		return ""
	}

	managedEntry := filepath.Join(staticDir, panelEntryName)
	if _, err := os.Stat(managedEntry); err == nil {
		return managedEntry
	}
	return filepath.Join(staticDir, managementAssetName)
}

func panelEntryExists(staticDir string) bool {
	for _, fileName := range []string{panelEntryName, managementAssetName} {
		info, err := os.Stat(filepath.Join(staticDir, fileName))
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func panelDistInstalled(staticDir string) bool {
	info, err := os.Stat(filepath.Join(staticDir, panelEntryName))
	return err == nil && !info.IsDir()
}

func localAssetHash(staticDir string, assetName string) string {
	if strings.EqualFold(assetName, panelDistAssetName) {
		data, err := os.ReadFile(filepath.Join(staticDir, panelDigestMarkerName))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}

	localHash, err := fileSHA256(filepath.Join(staticDir, managementAssetName))
	if err != nil {
		return ""
	}
	return localHash
}

// AssetPath resolves a path inside the managed control panel asset directory.
func AssetPath(configFilePath string, relativePath string) (string, bool) {
	dir := StaticDir(configFilePath)
	if strings.TrimSpace(dir) == "" {
		return "", false
	}

	relativePath = strings.ReplaceAll(relativePath, "\\", "/")
	relativePath = strings.TrimPrefix(relativePath, "/")
	if strings.TrimSpace(relativePath) == "" {
		return FilePath(configFilePath), true
	}
	if hasParentPathSegment(relativePath) {
		return "", false
	}

	cleanedRel := path.Clean("/" + relativePath)
	if cleanedRel == "/" {
		return FilePath(configFilePath), true
	}
	cleanedRel = strings.TrimPrefix(cleanedRel, "/")
	if cleanedRel == "." || cleanedRel == ".." || strings.HasPrefix(cleanedRel, "../") {
		return "", false
	}

	target := filepath.Join(dir, filepath.FromSlash(cleanedRel))
	rootAbs, errRoot := filepath.Abs(dir)
	targetAbs, errTarget := filepath.Abs(target)
	if errRoot != nil || errTarget != nil {
		return "", false
	}
	if targetAbs != rootAbs && !strings.HasPrefix(targetAbs, rootAbs+string(os.PathSeparator)) {
		return "", false
	}
	return target, true
}

// localPanelDistDir resolves a bundled or local panel build output directory.
// It checks Docker image bundle paths first, then panel/dist near the executable and working directory.
func localPanelDistDir(staticDir string) string {
	candidates := []string{}

	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "static"),
			filepath.Join(exeDir, "panel", "dist"),
		)
	}

	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "static"),
			filepath.Join(wd, "panel", "dist"),
		)
	}

	candidates = append(candidates, "/app/panel-dist", "/CLIProxyAPI/static")

	staticAbs, errStaticAbs := filepath.Abs(staticDir)
	for _, dir := range candidates {
		if staticAbs != "" && errStaticAbs == nil {
			if dirAbs, err := filepath.Abs(dir); err == nil && dirAbs == staticAbs {
				continue
			}
		}
		entry := filepath.Join(dir, panelEntryName)
		if info, err := os.Stat(entry); err == nil && !info.IsDir() {
			return dir
		}
	}

	return ""
}

// syncLocalPanelBuild checks for a local panel/dist build output and syncs it to the static directory.
// Returns true if a local build was synced (meaning no remote download is needed).
func syncLocalPanelBuild(staticDir string) bool {
	distDir := localPanelDistDir(staticDir)
	if distDir == "" {
		return false
	}

	localHash, err := dirSHA256(distDir)
	if err != nil {
		return false
	}

	markerPath := filepath.Join(staticDir, localPanelMarkerName)
	existingHash, _ := os.ReadFile(markerPath)
	if strings.TrimSpace(string(existingHash)) == localHash {
		log.Debug("local panel build is already synced")
		return panelEntryExists(staticDir)
	}

	if errMkdir := os.MkdirAll(staticDir, 0o755); errMkdir != nil {
		log.WithError(errMkdir).Warn("failed to prepare static directory for local panel sync")
		return false
	}

	entries, err := os.ReadDir(distDir)
	if err != nil {
		log.WithError(err).Warn("failed to read local panel dist directory")
		return false
	}

	for _, entry := range entries {
		src := filepath.Join(distDir, entry.Name())
		dst := filepath.Join(staticDir, entry.Name())
		if err := os.RemoveAll(dst); err != nil {
			log.WithError(err).Warnf("failed to remove stale static entry %s", entry.Name())
			return false
		}
		if entry.IsDir() {
			if err := copyDir(src, dst); err != nil {
				log.WithError(err).Warnf("failed to copy local panel directory %s", entry.Name())
				return false
			}
		} else if entry.Type().IsRegular() {
			if err := copyFile(src, dst); err != nil {
				log.WithError(err).Warnf("failed to copy local panel file %s", entry.Name())
				return false
			}
		}
	}

	if err := atomicWriteFile(markerPath, []byte(localHash+"\n")); err != nil {
		log.WithError(err).Warn("failed to persist local panel digest marker")
	}

	log.Info("local panel build synced successfully")
	return true
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else if entry.Type().IsRegular() {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func dirSHA256(dir string) (string, error) {
	h := sha256.New()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		h.Write([]byte(rel))
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EnsureLatestManagementHTML checks the latest management panel asset and updates the local copy when needed.
// It prefers the SPA zip bundle (panel-dist.zip) and falls back to the single management.html asset.
// It coalesces concurrent sync attempts and returns whether the asset exists after the sync attempt.
func EnsureLatestManagementHTML(ctx context.Context, staticDir string, proxyURL string, panelRepository string) bool {
	if ctx == nil {
		ctx = context.Background()
	}

	staticDir = strings.TrimSpace(staticDir)
	if staticDir == "" {
		log.Debug("management asset sync skipped: empty static directory")
		return false
	}

	if synced := syncLocalPanelBuild(staticDir); synced {
		return true
	}

	localPath := filepath.Join(staticDir, managementAssetName)

	_, _, _ = sfGroup.Do(localPath, func() (interface{}, error) {
		lastUpdateCheckMu.Lock()
		now := time.Now()
		timeSinceLastAttempt := now.Sub(lastUpdateCheckTime)
		if !lastUpdateCheckTime.IsZero() && timeSinceLastAttempt < managementSyncMinInterval {
			lastUpdateCheckMu.Unlock()
			log.Debugf(
				"management asset sync skipped by throttle: last attempt %v ago (interval %v)",
				timeSinceLastAttempt.Round(time.Second),
				managementSyncMinInterval,
			)
			return nil, nil
		}
		lastUpdateCheckTime = now
		lastUpdateCheckMu.Unlock()

		localFileMissing := !panelEntryExists(staticDir)

		if errMkdirAll := os.MkdirAll(staticDir, 0o755); errMkdirAll != nil {
			log.WithError(errMkdirAll).Warn("failed to prepare static directory for management asset")
			return nil, nil
		}

		releaseURL := resolveReleaseURL(panelRepository)
		client := newHTTPClient(proxyURL)

		asset, remoteHash, err := fetchLatestAsset(ctx, client, releaseURL)
		if err != nil {
			if localFileMissing {
				log.WithError(err).Warn("failed to fetch latest management release information, trying fallback page")
				if ensureFallbackManagementHTML(ctx, client, localPath) {
					return nil, nil
				}
				return nil, nil
			}
			log.WithError(err).Warn("failed to fetch latest management release information")
			return nil, nil
		}

		localHash := localAssetHash(staticDir, asset.Name)
		if remoteHash != "" && localHash != "" && strings.EqualFold(remoteHash, localHash) {
			if !strings.EqualFold(asset.Name, panelDistAssetName) || panelDistInstalled(staticDir) {
				log.Debug("management asset is already up to date")
				return nil, nil
			}
		}

		data, downloadedHash, err := downloadAsset(ctx, client, asset.BrowserDownloadURL)
		if err != nil {
			if localFileMissing {
				log.WithError(err).Warn("failed to download management asset, trying fallback page")
				if ensureFallbackManagementHTML(ctx, client, localPath) {
					return nil, nil
				}
				return nil, nil
			}
			log.WithError(err).Warn("failed to download management asset")
			return nil, nil
		}

		if remoteHash != "" && !strings.EqualFold(remoteHash, downloadedHash) {
			log.Errorf("management asset digest mismatch: expected %s got %s — aborting update for safety", remoteHash, downloadedHash)
			return nil, nil
		}

		if strings.EqualFold(asset.Name, panelDistAssetName) {
			if err = extractPanelDist(data, staticDir); err != nil {
				log.WithError(err).Warn("failed to extract management panel distribution")
				return nil, nil
			}
			if err = atomicWriteFile(filepath.Join(staticDir, panelDigestMarkerName), []byte(downloadedHash+"\n")); err != nil {
				log.WithError(err).Warn("failed to persist management panel distribution digest")
				return nil, nil
			}
		} else {
			if err = atomicWriteFile(localPath, data); err != nil {
				log.WithError(err).Warn("failed to update management asset on disk")
				return nil, nil
			}
		}

		log.Infof("management asset synced successfully (hash=%s)", downloadedHash)
		return nil, nil
	})

	return panelEntryExists(staticDir)
}

func ensureFallbackManagementHTML(ctx context.Context, client *http.Client, localPath string) bool {
	data, downloadedHash, err := downloadAsset(ctx, client, defaultManagementFallbackURL)
	if err != nil {
		log.WithError(err).Warn("failed to download fallback management control panel page")
		return false
	}

	log.Warnf("management asset downloaded from fallback URL without digest verification (hash=%s) — "+
		"keep disable-auto-update-panel set to false to enable verified GitHub panel asset sync", downloadedHash)

	if err = atomicWriteFile(localPath, data); err != nil {
		log.WithError(err).Warn("failed to persist fallback management control panel page")
		return false
	}

	log.Infof("management asset synced from fallback page successfully (hash=%s)", downloadedHash)
	return true
}

func extractPanelDist(data []byte, staticDir string) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open panel distribution archive: %w", err)
	}

	parentDir := filepath.Dir(staticDir)
	if err = os.MkdirAll(parentDir, 0o755); err != nil {
		return fmt.Errorf("prepare panel distribution temp parent: %w", err)
	}

	tempDir, err := os.MkdirTemp(parentDir, ".panel-dist-*")
	if err != nil {
		return fmt.Errorf("create panel distribution temp directory: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	var extractedSize uint64
	for _, file := range reader.File {
		relativePath, ok := cleanArchivePath(file.Name)
		if !ok {
			return fmt.Errorf("unsafe panel distribution path %q", file.Name)
		}
		if relativePath == "" {
			continue
		}

		targetPath := filepath.Join(tempDir, filepath.FromSlash(relativePath))
		info := file.FileInfo()
		if info.IsDir() {
			if err = os.MkdirAll(targetPath, 0o755); err != nil {
				return fmt.Errorf("create panel distribution directory %s: %w", relativePath, err)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported panel distribution entry %s", relativePath)
		}

		extractedSize += file.UncompressedSize64
		if extractedSize > maxExtractedPanelSize {
			return fmt.Errorf("panel distribution exceeds maximum expanded size of %d bytes", maxExtractedPanelSize)
		}

		if err = os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("create panel distribution parent for %s: %w", relativePath, err)
		}

		if err = extractArchiveFile(file, targetPath); err != nil {
			return err
		}
	}

	if _, err = os.Stat(filepath.Join(tempDir, panelEntryName)); err != nil {
		return fmt.Errorf("panel distribution missing %s: %w", panelEntryName, err)
	}

	if err = os.MkdirAll(staticDir, 0o755); err != nil {
		return fmt.Errorf("prepare panel static directory: %w", err)
	}

	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return fmt.Errorf("read extracted panel distribution: %w", err)
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(tempDir, entry.Name())
		targetPath := filepath.Join(staticDir, entry.Name())
		if err = os.RemoveAll(targetPath); err != nil {
			return fmt.Errorf("remove stale panel distribution entry %s: %w", entry.Name(), err)
		}
		if err = os.Rename(sourcePath, targetPath); err != nil {
			return fmt.Errorf("install panel distribution entry %s: %w", entry.Name(), err)
		}
	}

	return nil
}

func cleanArchivePath(fileName string) (string, bool) {
	fileName = strings.ReplaceAll(fileName, "\\", "/")
	if strings.Contains(fileName, "\x00") || strings.HasPrefix(fileName, "/") {
		return "", false
	}
	if hasParentPathSegment(fileName) {
		return "", false
	}

	cleaned := path.Clean(fileName)
	if cleaned == "." {
		return "", true
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

func hasParentPathSegment(fileName string) bool {
	for _, part := range strings.Split(fileName, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func extractArchiveFile(file *zip.File, targetPath string) error {
	source, err := file.Open()
	if err != nil {
		return fmt.Errorf("open panel distribution file %s: %w", file.Name, err)
	}
	defer func() {
		_ = source.Close()
	}()

	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create panel distribution file %s: %w", file.Name, err)
	}

	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil {
		return fmt.Errorf("write panel distribution file %s: %w", file.Name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close panel distribution file %s: %w", file.Name, closeErr)
	}
	return nil
}

func resolveReleaseURL(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return defaultManagementReleaseURL
	}

	parsed, err := url.Parse(repo)
	if err != nil || parsed.Host == "" {
		return defaultManagementReleaseURL
	}

	host := strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	if host == "api.github.com" {
		if !strings.HasSuffix(strings.ToLower(parsed.Path), "/releases/latest") {
			parsed.Path = parsed.Path + "/releases/latest"
		}
		return parsed.String()
	}

	if host == "github.com" {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			repoName := strings.TrimSuffix(parts[1], ".git")
			return fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", parts[0], repoName)
		}
	}

	return defaultManagementReleaseURL
}

func fetchLatestAsset(ctx context.Context, client *http.Client, releaseURL string) (*releaseAsset, string, error) {
	if strings.TrimSpace(releaseURL) == "" {
		releaseURL = defaultManagementReleaseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", httpUserAgent)
	gitURL := strings.ToLower(strings.TrimSpace(os.Getenv("GITSTORE_GIT_URL")))
	if tok := strings.TrimSpace(os.Getenv("GITSTORE_GIT_TOKEN")); tok != "" && strings.Contains(gitURL, "github.com") {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("execute release request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, "", fmt.Errorf("unexpected release status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var release releaseResponse
	if err = json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, "", fmt.Errorf("decode release response: %w", err)
	}

	for _, assetName := range []string{panelDistAssetName, managementAssetName} {
		for i := range release.Assets {
			asset := &release.Assets[i]
			if strings.EqualFold(asset.Name, assetName) {
				remoteHash := parseDigest(asset.Digest)
				return asset, remoteHash, nil
			}
		}
	}

	return nil, "", fmt.Errorf("management asset %s or %s not found in latest release", panelDistAssetName, managementAssetName)
}

func downloadAsset(ctx context.Context, client *http.Client, downloadURL string) ([]byte, string, error) {
	if strings.TrimSpace(downloadURL) == "" {
		return nil, "", fmt.Errorf("empty download url")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("User-Agent", httpUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("execute download request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, "", fmt.Errorf("unexpected download status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetDownloadSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("read download body: %w", err)
	}
	if int64(len(data)) > maxAssetDownloadSize {
		return nil, "", fmt.Errorf("download exceeds maximum allowed size of %d bytes", maxAssetDownloadSize)
	}

	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = file.Close()
	}()

	h := sha256.New()
	if _, err = io.Copy(h, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func atomicWriteFile(path string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), "management-*.html")
	if err != nil {
		return err
	}

	tmpName := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err = tmpFile.Write(data); err != nil {
		return err
	}

	if err = tmpFile.Chmod(0o644); err != nil {
		return err
	}

	if err = tmpFile.Close(); err != nil {
		return err
	}

	if err = os.Rename(tmpName, path); err != nil {
		return err
	}

	return nil
}

func parseDigest(digest string) string {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return ""
	}

	if idx := strings.Index(digest, ":"); idx >= 0 {
		digest = digest[idx+1:]
	}

	return strings.ToLower(strings.TrimSpace(digest))
}
