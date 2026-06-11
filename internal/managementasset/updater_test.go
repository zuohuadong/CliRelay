package managementasset

import (
<<<<<<< HEAD
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPanelDistAndResolveAssetPath(t *testing.T) {
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)

	archive := newPanelDistArchive(t, map[string]string{
		"manage.html":   "<!doctype html><script src=\"/manage/assets/app.js\"></script>",
		"assets/app.js": "console.log('panel')",
	})
	if err := extractPanelDist(archive, staticDir); err != nil {
		t.Fatalf("extractPanelDist() error = %v", err)
	}

	entryPath := FilePath("")
	if filepath.Base(entryPath) != panelEntryName {
		t.Fatalf("FilePath() = %q, want %s", entryPath, panelEntryName)
	}
	if _, err := os.Stat(entryPath); err != nil {
		t.Fatalf("stat extracted entry: %v", err)
	}

	assetPath, ok := AssetPath("", "/assets/app.js")
	if !ok {
		t.Fatal("AssetPath() rejected valid asset path")
	}
	if assetPath != filepath.Join(staticDir, "assets", "app.js") {
		t.Fatalf("AssetPath() = %q", assetPath)
	}
	if _, ok := AssetPath("", "../config.yaml"); ok {
		t.Fatal("AssetPath() accepted path traversal")
	}
}

func TestFetchLatestAssetPrefersPanelDist(t *testing.T) {
	archive := newPanelDistArchive(t, map[string]string{
		"manage.html": "<!doctype html>",
	})
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"assets":[{"name":"management.html","browser_download_url":"%s/management.html","digest":"sha256:legacy"},{"name":"panel-dist.zip","browser_download_url":"%s/panel-dist.zip","digest":"sha256:%s"}]}`, serverURL(r), serverURL(r), digest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := server.Client()
	asset, remoteHash, err := fetchLatestAsset(context.Background(), client, server.URL+"/release")
	if err != nil {
		t.Fatalf("fetchLatestAsset() error = %v", err)
	}
	if asset.Name != panelDistAssetName {
		t.Fatalf("asset.Name = %q, want %q", asset.Name, panelDistAssetName)
	}
	if remoteHash != digest {
		t.Fatalf("remoteHash = %q, want %q", remoteHash, digest)
	}
}

func TestSyncLocalPanelBuildUsesBundledStaticDirectory(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "bundle", "static")
	staticDir := filepath.Join(root, "data", "static")
	if err := os.MkdirAll(filepath.Join(sourceDir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir source assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, panelEntryName), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatalf("write source entry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "assets", "app.js"), []byte("console.log('new')"), 0o644); err != nil {
		t.Fatalf("write source asset: %v", err)
	}
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatalf("mkdir static dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, panelEntryName), []byte("old"), 0o644); err != nil {
		t.Fatalf("write old entry: %v", err)
	}

	t.Chdir(filepath.Join(root, "bundle"))

	if !syncLocalPanelBuild(staticDir) {
		t.Fatal("syncLocalPanelBuild() = false, want true")
	}

	copied, err := os.ReadFile(filepath.Join(staticDir, "assets", "app.js"))
	if err != nil {
		t.Fatalf("read copied asset: %v", err)
	}
	if string(copied) != "console.log('new')" {
		t.Fatalf("copied asset = %q", copied)
	}
	if _, err := os.Stat(filepath.Join(staticDir, localPanelMarkerName)); err != nil {
		t.Fatalf("stat local panel marker: %v", err)
	}
}

func TestLocalPanelDistDirSkipsTargetStaticDirectory(t *testing.T) {
	root := t.TempDir()
	staticDir := filepath.Join(root, "static")
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatalf("mkdir static dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, panelEntryName), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatalf("write static entry: %v", err)
	}

	t.Chdir(root)

	if got := localPanelDistDir(staticDir); got != "" {
		t.Fatalf("localPanelDistDir() = %q, want empty when candidate is target static dir", got)
	}
}

func newPanelDistArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, body := range files {
		fileWriter, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create archive entry %s: %v", name, err)
		}
		if _, err = fileWriter.Write([]byte(body)); err != nil {
			t.Fatalf("write archive entry %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return buf.Bytes()
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
=======
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestAutoUpdateSkipReason(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		wantReason string
		wantSkip   bool
	}{
		{
			name:       "nil config",
			cfg:        nil,
			wantReason: "config not yet available",
			wantSkip:   true,
		},
		{
			name: "cluster mode",
			cfg: &config.Config{
				Home: config.HomeConfig{Enabled: true},
			},
			wantReason: "cluster mode enabled",
			wantSkip:   true,
		},
		{
			name: "control panel disabled",
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{DisableControlPanel: true},
			},
			wantReason: "control panel disabled",
			wantSkip:   true,
		},
		{
			name: "auto update disabled",
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{DisableAutoUpdatePanel: true},
			},
			wantReason: "disable-auto-update-panel is enabled",
			wantSkip:   true,
		},
		{
			name:       "enabled",
			cfg:        &config.Config{},
			wantReason: "",
			wantSkip:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReason, gotSkip := autoUpdateSkipReason(tt.cfg)
			if gotReason != tt.wantReason || gotSkip != tt.wantSkip {
				t.Fatalf("autoUpdateSkipReason() = (%q, %t), want (%q, %t)", gotReason, gotSkip, tt.wantReason, tt.wantSkip)
			}
		})
	}
}
>>>>>>> upstream/main
