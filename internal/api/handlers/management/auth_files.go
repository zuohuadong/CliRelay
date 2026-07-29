package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

var lastRefreshKeys = []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"}

const (
	anthropicCallbackPort = 54545
	codexCallbackPort     = 1455
)

type callbackForwarder struct {
	provider string
	server   *http.Server
	done     chan struct{}
	address  string
}

type codexOAuthService interface {
	GenerateAuthURL(state string, pkceCodes *codex.PKCECodes) (string, error)
	ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error)
	CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage
}

var (
	callbackForwardersMu   sync.Mutex
	callbackForwarders     = make(map[int]*callbackForwarder)
	codexTokenBindingLocks keyedMutexTable
	errAuthFileMustBeJSON  = errors.New("auth file must be .json")
	errAuthFileNotFound    = errors.New("auth file not found")
	errPluginVirtualAuth   = errors.New("plugin virtual auth cannot be modified directly; edit or delete the source auth file")
	errFileBundleAuth      = errors.New("multi-account auth bundle member cannot be modified directly; edit the source auth file")
	newCodexOAuthService   = func(_ *config.Config, client *http.Client) codexOAuthService {
		return codex.NewCodexAuthWithHTTPClient(client)
	}
)

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

type keyedMutexTable struct {
	mu      sync.Mutex
	entries map[string]*keyedMutexEntry
}

func (t *keyedMutexTable) lock(keys ...string) func() {
	if t == nil {
		return func() {}
	}
	unique := make(map[string]struct{}, len(keys))
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return func() {}
	}
	sort.Strings(normalized)

	t.mu.Lock()
	if t.entries == nil {
		t.entries = make(map[string]*keyedMutexEntry)
	}
	entries := make([]*keyedMutexEntry, len(normalized))
	for i, key := range normalized {
		entry := t.entries[key]
		if entry == nil {
			entry = &keyedMutexEntry{}
			t.entries[key] = entry
		}
		entry.refs++
		entries[i] = entry
	}
	t.mu.Unlock()

	for _, entry := range entries {
		entry.mu.Lock()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(entries) - 1; i >= 0; i-- {
				entries[i].mu.Unlock()
			}
			t.mu.Lock()
			for i, key := range normalized {
				entry := entries[i]
				entry.refs--
				if entry.refs == 0 && t.entries[key] == entry {
					delete(t.entries, key)
				}
			}
			t.mu.Unlock()
		})
	}
}

func extractLastRefreshTimestamp(meta map[string]any) (time.Time, bool) {
	if len(meta) == 0 {
		return time.Time{}, false
	}
	for _, key := range lastRefreshKeys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseLastRefreshValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func parseLastRefreshValue(v any) (time.Time, bool) {
	switch val := v.(type) {
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC(), true
			}
		}
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0).UTC(), true
		}
	case float64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case int64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(val, 0).UTC(), true
	case int:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case json.Number:
		if i, err := val.Int64(); err == nil && i > 0 {
			return time.Unix(i, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func isWebUIRequest(c *gin.Context) bool {
	raw := strings.TrimSpace(c.Query("is_webui"))
	if raw == "" {
		return false
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func startCallbackForwarder(port int, provider, targetBase string) (*callbackForwarder, error) {
	callbackForwardersMu.Lock()
	prev := callbackForwarders[port]
	if prev != nil {
		delete(callbackForwarders, port)
	}
	callbackForwardersMu.Unlock()

	if prev != nil {
		stopForwarderInstance(port, prev)
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := targetBase
		if raw := r.URL.RawQuery; raw != "" {
			if strings.Contains(target, "?") {
				target = target + "&" + raw
			} else {
				target = target + "?" + raw
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	})

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	done := make(chan struct{})

	go func() {
		if errServe := srv.Serve(ln); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.WithError(errServe).Warnf("callback forwarder for %s stopped unexpectedly", provider)
		}
		close(done)
	}()

	forwarder := &callbackForwarder{
		provider: provider,
		server:   srv,
		done:     done,
		address:  ln.Addr().String(),
	}

	callbackForwardersMu.Lock()
	callbackForwarders[port] = forwarder
	callbackForwardersMu.Unlock()

	log.Infof("callback forwarder for %s listening on %s", provider, addr)

	return forwarder, nil
}

func stopCallbackForwarderInstance(port int, forwarder *callbackForwarder) {
	if forwarder == nil {
		return
	}
	callbackForwardersMu.Lock()
	if current := callbackForwarders[port]; current == forwarder {
		delete(callbackForwarders, port)
	}
	callbackForwardersMu.Unlock()

	stopForwarderInstance(port, forwarder)
}

func stopForwarderInstance(port int, forwarder *callbackForwarder) {
	if forwarder == nil || forwarder.server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := forwarder.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.WithError(err).Warnf("failed to shut down callback forwarder on port %d", port)
	}

	select {
	case <-forwarder.done:
	case <-time.After(2 * time.Second):
	}

	log.Infof("callback forwarder on port %d stopped", port)
}

func (h *Handler) managementCallbackURL(path string) (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	scheme := "http"
	if h.cfg.TLS.Enable {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, h.cfg.Port, path), nil
}

func pluginAuthProviderFromPath(path string) (string, bool) {
	path = strings.TrimSpace(path)
	const prefix = "/v0/management/"
	const suffix = "-auth-url"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	provider := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return "", false
	}
	for _, r := range provider {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return "", false
		}
	}
	return provider, true
}

func (h *Handler) ServePluginAuthURL(c *gin.Context) bool {
	if h == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if host == nil {
		return false
	}
	provider, ok := pluginAuthProviderFromPath(c.Request.URL.Path)
	if !ok || !host.HasAuthProvider(provider) {
		return false
	}

	ctx := PopulateAuthContext(context.Background(), c)
	baseURL, errBaseURL := h.managementCallbackURL("/v0/management/oauth-callback")
	if errBaseURL != nil {
		log.WithError(errBaseURL).Error("failed to compute plugin auth callback URL")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	resp, handled, errStart := host.StartLogin(ctx, provider, baseURL)
	if !handled {
		return false
	}
	if errStart != nil {
		log.WithError(errStart).Error("failed to start plugin auth login")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	state := strings.TrimSpace(resp.State)
	if state == "" {
		log.WithField("provider", provider).Error("plugin auth provider returned empty state")
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid oauth state"})
		return true
	}
	if errState := ValidateOAuthState(state); errState != nil {
		log.WithError(errState).WithField("provider", provider).Error("plugin auth provider returned invalid state")
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid oauth state"})
		return true
	}
	if errRegister := RegisterPluginOAuthSession(state, provider, resp.Metadata); errRegister != nil {
		log.WithError(errRegister).WithField("provider", provider).Error("failed to register plugin oauth session")
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": resp.URL, "state": state})
	return true
}

func (h *Handler) ListAuthFiles(c *gin.Context) {
	if h == nil {
		c.JSON(500, gin.H{"error": "handler not initialized"})
		return
	}
	if h.authManager == nil {
		h.listAuthFilesFromDisk(c)
		return
	}
	auths := h.authManager.List()
	files := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if entry := h.buildAuthFileEntry(auth); entry != nil {
			files = append(files, entry)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		nameI, _ := files[i]["name"].(string)
		nameJ, _ := files[j]["name"].(string)
		return strings.ToLower(nameI) < strings.ToLower(nameJ)
	})
	c.JSON(200, gin.H{"files": files})
}

// GetAuthFileModels returns the models supported by a specific auth file
func (h *Handler) GetAuthFileModels(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		c.JSON(400, gin.H{"error": "name is required"})
		return
	}

	// Try to find auth ID via authManager
	var authID string
	if h.authManager != nil {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name || auth.ID == name {
				authID = auth.ID
				break
			}
		}
	}

	if authID == "" {
		authID = name // fallback to filename as ID
	}

	// Get models from registry
	reg := registry.GetGlobalRegistry()
	models := reg.GetModelsForClient(authID)

	result := make([]gin.H, 0, len(models))
	for _, m := range models {
		entry := gin.H{
			"id": m.ID,
		}
		if m.DisplayName != "" {
			entry["display_name"] = m.DisplayName
		}
		if m.Type != "" {
			entry["type"] = m.Type
		}
		if m.OwnedBy != "" {
			entry["owned_by"] = m.OwnedBy
		}
		result = append(result, entry)
	}

	c.JSON(200, gin.H{"models": result})
}

// List auth files from disk when the auth manager is unavailable.
func (h *Handler) listAuthFilesFromDisk(c *gin.Context) {
	entries, err := os.ReadDir(h.cfg.AuthDir)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
		return
	}
	files := make([]gin.H, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		if info, errInfo := e.Info(); errInfo == nil {
			fileData := gin.H{"name": name, "size": info.Size(), "modtime": info.ModTime()}

			// Read file to get type field
			full := filepath.Join(h.cfg.AuthDir, name)
			if data, errRead := os.ReadFile(full); errRead == nil {
				typeValue := gjson.GetBytes(data, "type").String()
				emailValue := gjson.GetBytes(data, "email").String()
				fileData["type"] = typeValue
				fileData["email"] = emailValue
				var metadata map[string]any
				if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal == nil {
					disabled, disabledOK := metadata["disabled"].(bool)
					if disabledOK {
						fileData["disabled"] = disabled
					}
					addAuthFileTokenHealth(fileData, typeValue, metadata, disabled, time.Now())
				}
				if projectID := strings.TrimSpace(gjson.GetBytes(data, "project_id").String()); projectID != "" {
					fileData["project_id"] = projectID
				}
				if pv := gjson.GetBytes(data, "priority"); pv.Exists() {
					switch pv.Type {
					case gjson.Number:
						fileData["priority"] = int(pv.Int())
					case gjson.String:
						if parsed, errAtoi := strconv.Atoi(strings.TrimSpace(pv.String())); errAtoi == nil {
							fileData["priority"] = parsed
						}
					}
				}
				if nv := gjson.GetBytes(data, "note"); nv.Exists() && nv.Type == gjson.String {
					if trimmed := strings.TrimSpace(nv.String()); trimmed != "" {
						fileData["note"] = trimmed
					}
				}
				if wv := gjson.GetBytes(data, "websockets"); wv.Exists() {
					switch wv.Type {
					case gjson.True:
						fileData["websockets"] = true
					case gjson.False:
						fileData["websockets"] = false
					case gjson.String:
						if parsed, errParse := strconv.ParseBool(strings.TrimSpace(wv.String())); errParse == nil {
							fileData["websockets"] = parsed
						}
					}
				}
			}

			files = append(files, fileData)
		}
	}
	c.JSON(200, gin.H{"files": files})
}

func (h *Handler) buildAuthFileEntry(auth *coreauth.Auth) gin.H {
	if auth == nil {
		return nil
	}
	auth.EnsureIndex()
	runtimeOnly := isRuntimeOnlyAuth(auth)
	if runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
		return nil
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" && !runtimeOnly {
		return nil
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	providerName := strings.TrimSpace(auth.Provider)
	if cn := strings.TrimSpace(auth.Attributes["compat_name"]); cn != "" {
		providerName = cn
	}

	entry := gin.H{
		"id":             auth.ID,
		"auth_index":     auth.Index,
		"name":           name,
		"type":           strings.TrimSpace(auth.Provider),
		"provider":       strings.TrimSpace(auth.Provider),
		"provider_name":  providerName,
		"label":          auth.Label,
		"status":         auth.Status,
		"status_message": auth.StatusMessage,
		"disabled":       auth.Disabled,
		"unavailable":    auth.Unavailable,
		"runtime_only":   runtimeOnly,
		"source":         "memory",
		"size":           int64(0),
	}
	tags := buildAuthTagPayload(auth)
	entry["channel_name"] = auth.ChannelName()
	entry["default_tags"] = tags.DefaultTags
	entry["custom_tags"] = tags.CustomTags
	entry["hidden_default_tags"] = tags.HiddenDefaultTags
	entry["display_tags"] = tags.DisplayTags
	entry["success"] = auth.Success
	entry["failed"] = auth.Failed
	entry["recent_requests"] = auth.RecentRequestsSnapshot(time.Now())
	entry["quota"] = gin.H{"exceeded": auth.Quota.Exceeded, "reason": auth.Quota.Reason, "next_recover_at": auth.Quota.NextRecoverAt, "backoff_level": auth.Quota.BackoffLevel}
	if baseURL := strings.TrimSpace(auth.Attributes["base_url"]); baseURL != "" {
		entry["base_url"] = baseURL
	}
	if email := authEmail(auth); email != "" {
		entry["email"] = email
	}
	if projectID := authProjectID(auth); projectID != "" {
		entry["project_id"] = projectID
	}
	if accountType, account := auth.AccountInfo(); accountType != "" || account != "" {
		if accountType != "" {
			entry["account_type"] = accountType
		}
		if account != "" {
			entry["account"] = account
		}
	}
	if !auth.CreatedAt.IsZero() {
		entry["created_at"] = auth.CreatedAt
	}
	if !auth.UpdatedAt.IsZero() {
		entry["modtime"] = auth.UpdatedAt
		entry["updated_at"] = auth.UpdatedAt
	}
	if !auth.LastRefreshedAt.IsZero() {
		entry["last_refresh"] = auth.LastRefreshedAt
	}
	if auth.Metadata != nil {
		if lastRefresh, ok := extractLastRefreshTimestamp(auth.Metadata); ok && !lastRefresh.IsZero() {
			entry["last_refresh"] = lastRefresh
		}
	}
	addAuthFileTokenHealth(entry, auth.Provider, auth.Metadata, auth.Disabled, time.Now())
	if !auth.NextRetryAfter.IsZero() {
		entry["next_retry_after"] = auth.NextRetryAfter
	}
	if path != "" {
		entry["path"] = path
		entry["source"] = "file"
		if info, err := os.Stat(path); err == nil {
			entry["size"] = info.Size()
			entry["modtime"] = info.ModTime()
		} else if os.IsNotExist(err) {
			// Hide credentials removed from disk but still lingering in memory.
			if !runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled || strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "removed via management api")) {
				return nil
			}
			entry["source"] = "memory"
		} else {
			log.WithError(err).Warnf("failed to stat auth file %s", path)
		}
	}
	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		entry["id_token"] = claims
	}
	// Expose priority from Attributes (set by synthesizer from JSON "priority" field).
	// Fall back to Metadata for auths registered via UploadAuthFile (no synthesizer).
	if p := strings.TrimSpace(authAttribute(auth, "priority")); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			entry["priority"] = parsed
		}
	} else if auth.Metadata != nil {
		if rawPriority, ok := auth.Metadata["priority"]; ok {
			switch v := rawPriority.(type) {
			case float64:
				entry["priority"] = int(v)
			case int:
				entry["priority"] = v
			case string:
				if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					entry["priority"] = parsed
				}
			}
		}
	}
	// Expose note from Attributes (set by synthesizer from JSON "note" field).
	// Fall back to Metadata for auths registered via UploadAuthFile (no synthesizer).
	if note := strings.TrimSpace(authAttribute(auth, "note")); note != "" {
		entry["note"] = note
	} else if auth.Metadata != nil {
		if rawNote, ok := auth.Metadata["note"].(string); ok {
			if trimmed := strings.TrimSpace(rawNote); trimmed != "" {
				entry["note"] = trimmed
			}
		}
	}
	if websockets, ok := authWebsocketsValue(auth); ok {
		entry["websockets"] = websockets
	}
	if codexFastMode, ok := authCodexFastModeValue(auth); ok {
		entry["codex_fast_mode"] = codexFastMode
	}
	if auth.Metadata != nil {
		if models, ok := auth.Metadata["models"]; ok {
			entry["models"] = models
		}
	}
	return entry
}

func addAuthFileTokenHealth(entry gin.H, provider string, metadata map[string]any, disabled bool, now time.Time) {
	if entry == nil || metadata == nil {
		return
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != "codex" {
		return
	}
	auth := &coreauth.Auth{Metadata: metadata}
	expiresAt, ok := auth.ExpirationTime()
	if !ok {
		return
	}
	expiresAt = expiresAt.UTC()
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	secondsLeft := int64(expiresAt.Sub(now).Seconds())
	daysLeft := secondsLeft / int64(24*time.Hour/time.Second)
	if secondsLeft < 0 {
		daysLeft = -(((-secondsLeft) + int64(24*time.Hour/time.Second) - 1) / int64(24*time.Hour/time.Second))
	}

	health := "ok"
	switch {
	case disabled:
		health = "disabled"
	case !expiresAt.After(now):
		health = "expired"
	case expiresAt.Sub(now) <= 24*time.Hour:
		health = "critical"
	case expiresAt.Sub(now) <= 72*time.Hour:
		health = "warning"
	}

	entry["token_health"] = health
	entry["token_expires_at"] = expiresAt
	entry["token_expires_at_ms"] = expiresAt.UnixMilli()
	entry["token_seconds_left"] = secondsLeft
	entry["token_days_left"] = daysLeft
	if lastRefresh, ok := extractLastRefreshTimestamp(metadata); ok && !lastRefresh.IsZero() {
		entry["token_last_refresh"] = lastRefresh.UTC()
		entry["token_last_refresh_ms"] = lastRefresh.UTC().UnixMilli()
	}
}

func authCodexFastModeValue(auth *coreauth.Auth) (bool, bool) {
	if auth == nil {
		return false, false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false, false
	}
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["codex_fast_mode"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed, true
			}
		}
	}
	if auth.Metadata == nil {
		return false, true
	}
	raw, ok := auth.Metadata["codex_fast_mode"]
	if !ok || raw == nil {
		return false, true
	}
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, true
}

func authWebsocketsValue(auth *coreauth.Auth) (bool, bool) {
	if auth == nil {
		return false, false
	}
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed, true
			}
		}
	}
	if auth.Metadata == nil {
		return false, false
	}
	raw, ok := auth.Metadata["websockets"]
	if !ok || raw == nil {
		return false, false
	}
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func authProjectID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["project_id"].(string); ok {
			if projectID := strings.TrimSpace(v); projectID != "" {
				return projectID
			}
		}
	}
	if auth.Attributes != nil {
		if projectID := strings.TrimSpace(auth.Attributes["project_id"]); projectID != "" {
			return projectID
		}
	}
	return ""
}

func extractCodexIDTokenClaims(auth *coreauth.Auth) gin.H {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return nil
	}
	idTokenRaw, ok := auth.Metadata["id_token"].(string)
	if !ok {
		return nil
	}
	idToken := strings.TrimSpace(idTokenRaw)
	if idToken == "" {
		return nil
	}
	claims, err := codex.ParseJWTToken(idToken)
	if err != nil || claims == nil {
		return nil
	}

	result := gin.H{}
	if v := strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID); v != "" {
		result["chatgpt_account_id"] = v
	}
	if v := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); v != "" {
		result["plan_type"] = v
	}
	if v := claims.CodexAuthInfo.ChatgptSubscriptionActiveStart; v != nil {
		result["chatgpt_subscription_active_start"] = v
	}
	if v := claims.CodexAuthInfo.ChatgptSubscriptionActiveUntil; v != nil {
		result["chatgpt_subscription_active_until"] = v
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func authEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["email"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["email"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(auth.Attributes["account_email"]); v != "" {
			return v
		}
	}
	return ""
}

func authAttribute(auth *coreauth.Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return auth.Attributes[key]
}

func isRuntimeOnlyAuth(auth *coreauth.Auth) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}

func isUnsafeAuthFileName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return true
	}
	if strings.ContainsAny(name, "/\\") {
		return true
	}
	if filepath.VolumeName(name) != "" {
		return true
	}
	return false
}

// Download single auth file by name
func (h *Handler) DownloadAuthFile(c *gin.Context) {
	if !requireAdminManagementCredential(c) {
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if isUnsafeAuthFileName(name) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		c.JSON(400, gin.H{"error": "name must end with .json"})
		return
	}
	full := filepath.Join(h.cfg.AuthDir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(404, gin.H{"error": "file not found"})
		} else {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read file: %v", err)})
		}
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	c.Data(200, "application/json", data)
}

// Upload auth file: multipart or raw JSON with ?name=
func (h *Handler) UploadAuthFile(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx := c.Request.Context()
	// Optional: bind imported codex auths to a channel egress endpoint so they
	// are ready for strict-egress traffic immediately after import, without a
	// separate visit to the egress bindings panel.
	egressID := strings.TrimSpace(c.Query("egress_id"))

	fileHeaders, errMultipart := h.multipartAuthFileHeaders(c)
	if errMultipart != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid multipart form: %v", errMultipart)})
		return
	}
	if len(fileHeaders) == 1 {
		uploadedName, errUpload := h.storeUploadedAuthFile(ctx, fileHeaders[0])
		if errUpload != nil {
			if errors.Is(errUpload, errAuthFileMustBeJSON) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "file must be .json"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": errUpload.Error()})
			return
		}
		bindingError := ""
		if egressID != "" {
			if errBind := h.bindImportedCodexAuthToEgress(ctx, egressID, uploadedName); errBind != nil {
				bindingError = errBind.Error()
			}
		}
		resp := gin.H{"status": "ok"}
		if egressID != "" {
			resp["bound_egress"] = egressID
		}
		if bindingError != "" {
			resp["binding_error"] = bindingError
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	if len(fileHeaders) > 1 {
		uploaded := make([]string, 0, len(fileHeaders))
		failed := make([]gin.H, 0)
		bindingFailed := make([]gin.H, 0)
		for _, file := range fileHeaders {
			name, errUpload := h.storeUploadedAuthFile(ctx, file)
			if errUpload != nil {
				failureName := ""
				if file != nil {
					failureName = filepath.Base(file.Filename)
				}
				msg := errUpload.Error()
				if errors.Is(errUpload, errAuthFileMustBeJSON) {
					msg = "file must be .json"
				}
				failed = append(failed, gin.H{"name": failureName, "error": msg})
				continue
			}
			uploaded = append(uploaded, name)
			if egressID != "" {
				if errBind := h.bindImportedCodexAuthToEgress(ctx, egressID, name); errBind != nil {
					bindingFailed = append(bindingFailed, gin.H{"name": name, "error": errBind.Error()})
				}
			}
		}
		if len(failed) > 0 {
			c.JSON(http.StatusMultiStatus, gin.H{
				"status":   "partial",
				"uploaded": len(uploaded),
				"files":    uploaded,
				"failed":   failed,
			})
			return
		}
		resp := gin.H{"status": "ok", "uploaded": len(uploaded), "files": uploaded}
		if egressID != "" {
			resp["bound_egress"] = egressID
		}
		if len(bindingFailed) > 0 {
			resp["binding_failed"] = bindingFailed
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	if c.ContentType() == "multipart/form-data" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files uploaded"})
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if isUnsafeAuthFileName(name) {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		c.JSON(400, gin.H{"error": "name must end with .json"})
		return
	}
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	if err = h.writeAuthFile(ctx, filepath.Base(name), data); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	bindingError := ""
	if egressID != "" {
		if errBind := h.bindImportedCodexAuthToEgress(ctx, egressID, filepath.Base(name)); errBind != nil {
			bindingError = errBind.Error()
		}
	}
	resp := gin.H{"status": "ok"}
	if egressID != "" {
		resp["bound_egress"] = egressID
	}
	if bindingError != "" {
		resp["binding_error"] = bindingError
	}
	c.JSON(200, resp)
}

// Delete auth files: single by name or all
func (h *Handler) DeleteAuthFile(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	ctx := c.Request.Context()
	if all := c.Query("all"); all == "true" || all == "1" || all == "*" {
		entries, err := os.ReadDir(h.cfg.AuthDir)
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
			return
		}
		deleted := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
			full := filepath.Join(h.cfg.AuthDir, name)
			if !filepath.IsAbs(full) {
				if abs, errAbs := filepath.Abs(full); errAbs == nil {
					full = abs
				}
			}
			if err = os.Remove(full); err == nil {
				if errDel := h.deleteTokenRecord(ctx, full); errDel != nil {
					c.JSON(500, gin.H{"error": errDel.Error()})
					return
				}
				deleted++
				h.removeAuth(ctx, full)
			}
		}
		c.JSON(200, gin.H{"status": "ok", "deleted": deleted})
		return
	}

	names, errNames := requestedAuthFileNamesForDelete(c)
	if errNames != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNames.Error()})
		return
	}
	if len(names) == 0 {
		c.JSON(400, gin.H{"error": "invalid name"})
		return
	}
	if len(names) == 1 {
		if _, status, errDelete := h.deleteAuthFileByName(ctx, names[0]); errDelete != nil {
			c.JSON(status, gin.H{"error": errDelete.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	deletedFiles := make([]string, 0, len(names))
	failed := make([]gin.H, 0)
	for _, name := range names {
		deletedName, _, errDelete := h.deleteAuthFileByName(ctx, name)
		if errDelete != nil {
			failed = append(failed, gin.H{"name": name, "error": errDelete.Error()})
			continue
		}
		deletedFiles = append(deletedFiles, deletedName)
	}
	if len(failed) > 0 {
		c.JSON(http.StatusMultiStatus, gin.H{
			"status":  "partial",
			"deleted": len(deletedFiles),
			"files":   deletedFiles,
			"failed":  failed,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted": len(deletedFiles), "files": deletedFiles})
}

func (h *Handler) multipartAuthFileHeaders(c *gin.Context) ([]*multipart.FileHeader, error) {
	if h == nil || c == nil || c.ContentType() != "multipart/form-data" {
		return nil, nil
	}
	form, err := c.MultipartForm()
	if err != nil {
		return nil, err
	}
	if form == nil || len(form.File) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(form.File))
	for key := range form.File {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	headers := make([]*multipart.FileHeader, 0)
	for _, key := range keys {
		headers = append(headers, form.File[key]...)
	}
	return headers, nil
}

func (h *Handler) storeUploadedAuthFile(ctx context.Context, file *multipart.FileHeader) (string, error) {
	if file == nil {
		return "", fmt.Errorf("no file uploaded")
	}
	name := filepath.Base(strings.TrimSpace(file.Filename))
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return "", errAuthFileMustBeJSON
	}
	src, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("failed to open uploaded file: %w", err)
	}
	defer src.Close()

	data, err := io.ReadAll(src)
	if err != nil {
		return "", fmt.Errorf("failed to read uploaded file: %w", err)
	}
	if err := h.writeAuthFile(ctx, name, data); err != nil {
		return "", err
	}
	return name, nil
}

func (h *Handler) writeAuthFile(ctx context.Context, name string, data []byte) error {
	dst := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}
	auths, err := h.buildAuthsFromFileData(dst, data)
	if err != nil {
		return err
	}
	// Persist a JWT-derived account_id back into the auth file so egress binding
	// resolution, reset credits and request headers survive restarts without a
	// refresh round-trip. Imported/pasted codex auths often lack a top-level
	// account_id even though they carry a valid id_token.
	written := data
	if len(auths) > 0 && strings.EqualFold(strings.TrimSpace(auths[0].Provider), "codex") {
		if backfilled, changed := backfillCodexAccountIDInData(data); changed {
			written = backfilled
		}
	}
	if errWrite := os.WriteFile(dst, written, 0o600); errWrite != nil {
		return fmt.Errorf("failed to write file: %w", errWrite)
	}
	for _, auth := range auths {
		if err := h.upsertAuthRecord(ctx, auth); err != nil {
			return err
		}
	}
	return nil
}

// backfillCodexAccountIDInData parses an auth JSON payload, and when it is a
// codex auth missing account_id but carrying a valid id_token, injects the
// JWT-derived account_id and returns the re-marshaled bytes. It returns the
// original bytes unchanged when no backfill is needed or the payload cannot be
// parsed.
func backfillCodexAccountIDInData(data []byte) ([]byte, bool) {
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil || metadata == nil {
		return data, false
	}
	provider, _ := metadata["type"].(string)
	if provider == "" {
		provider, _ = metadata["provider"].(string)
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return data, false
	}
	if existing, _ := metadata["account_id"].(string); strings.TrimSpace(existing) != "" {
		return data, false
	}
	if codex.AccountIDFromMetadata(metadata) == "" {
		return data, false
	}
	backfilled, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return data, false
	}
	return backfilled, true
}

// bindImportedCodexAuthToEgress binds an imported codex auth file to the given
// channel egress endpoint. It is a no-op for non-codex auths. It resolves the
// account_id (with JWT backfill), validates endpoint readiness, and records the
// binding so strict-egress traffic works immediately after import. Callers may
// ignore the returned error to treat binding as best-effort on top of a
// successful import.
func (h *Handler) bindImportedCodexAuthToEgress(ctx context.Context, egressID, authFileName string) error {
	egressID = strings.TrimSpace(egressID)
	if egressID == "" {
		return nil
	}
	service := h.egress()
	if service == nil {
		return fmt.Errorf("%w: egress network is unavailable", egress.ErrEgressRequired)
	}
	auth := h.findAuthForDelete(authFileName)
	if auth == nil {
		return fmt.Errorf("auth %q not found after import", authFileName)
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	accountID := codex.AccountIDFromMetadata(auth.Metadata)
	if accountID == "" {
		return fmt.Errorf("codex auth %q has no account_id; refresh or re-login before binding", authFileName)
	}
	identity, err := egress.StableIdentity(accountID)
	if err != nil {
		return fmt.Errorf("derive codex egress identity: %w", err)
	}
	readiness, err := service.EndpointReadiness(ctx, egressID)
	if err != nil {
		return fmt.Errorf("egress endpoint %s: %w", egressID, err)
	}
	if !readiness.RuntimeReady {
		return fmt.Errorf("egress endpoint %s is not runtime ready: %s", egressID, strings.Join(readiness.Reasons, ","))
	}
	if err := service.PutBinding(ctx, egress.Binding{Identity: identity, EndpointID: egressID, AuthFileID: auth.ID}); err != nil {
		return fmt.Errorf("bind codex egress endpoint: %w", err)
	}
	return nil
}

func requestedAuthFileNamesForDelete(c *gin.Context) ([]string, error) {
	if c == nil {
		return nil, nil
	}
	names := uniqueAuthFileNames(c.QueryArray("name"))
	if len(names) > 0 {
		return names, nil
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}

	var objectBody struct {
		Name  string   `json:"name"`
		Names []string `json:"names"`
	}
	if body[0] == '[' {
		var arrayBody []string
		if err := json.Unmarshal(body, &arrayBody); err != nil {
			return nil, fmt.Errorf("invalid request body")
		}
		return uniqueAuthFileNames(arrayBody), nil
	}
	if err := json.Unmarshal(body, &objectBody); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}

	out := make([]string, 0, len(objectBody.Names)+1)
	if strings.TrimSpace(objectBody.Name) != "" {
		out = append(out, objectBody.Name)
	}
	out = append(out, objectBody.Names...)
	return uniqueAuthFileNames(out), nil
}

func uniqueAuthFileNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func (h *Handler) deleteAuthFileByName(ctx context.Context, name string) (string, int, error) {
	name = strings.TrimSpace(name)
	if isUnsafeAuthFileName(name) {
		return "", http.StatusBadRequest, fmt.Errorf("invalid name")
	}

	targetPath := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	targetID := ""
	if targetAuth := h.findAuthForDelete(name); targetAuth != nil {
		if !isPluginVirtualSourceDelete(name, targetAuth) {
			return filepath.Base(name), http.StatusConflict, errPluginVirtualAuth
		}
		targetID = strings.TrimSpace(targetAuth.ID)
		if path := strings.TrimSpace(authAttribute(targetAuth, "path")); path != "" {
			targetPath = path
		}
	}
	if !filepath.IsAbs(targetPath) {
		if abs, errAbs := filepath.Abs(targetPath); errAbs == nil {
			targetPath = abs
		}
	}
	if errRemove := os.Remove(targetPath); errRemove != nil {
		if os.IsNotExist(errRemove) {
			return filepath.Base(name), http.StatusNotFound, errAuthFileNotFound
		}
		return filepath.Base(name), http.StatusInternalServerError, fmt.Errorf("failed to remove file: %w", errRemove)
	}
	if errDeleteRecord := h.deleteTokenRecord(ctx, targetPath); errDeleteRecord != nil {
		return filepath.Base(name), http.StatusInternalServerError, errDeleteRecord
	}
	h.removeAuthsForPath(ctx, targetPath, targetID)
	return filepath.Base(name), http.StatusOK, nil
}

func isPluginVirtualSourceDelete(name string, auth *coreauth.Auth) bool {
	if !coreauth.IsPluginVirtualAuth(auth) {
		return true
	}
	sourcePath := strings.TrimSpace(authAttribute(auth, coreauth.AttributeVirtualSource))
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(authAttribute(auth, "path"))
	}
	if sourcePath == "" {
		return false
	}
	return strings.EqualFold(filepath.Base(strings.TrimSpace(name)), filepath.Base(sourcePath))
}

func (h *Handler) findAuthForDelete(name string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if auth, ok := h.authManager.GetByID(name); ok {
		return auth
	}
	auths := h.authManager.List()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.FileName) == name {
			return auth
		}
		if filepath.Base(strings.TrimSpace(authAttribute(auth, "path"))) == name {
			return auth
		}
	}
	return nil
}

func (h *Handler) authIDForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		if abs, errAbs := filepath.Abs(path); errAbs == nil {
			path = abs
		}
	}
	id := path
	if h != nil && h.cfg != nil {
		authDir := strings.TrimSpace(h.cfg.AuthDir)
		if resolvedAuthDir, errResolve := util.ResolveAuthDir(authDir); errResolve == nil && resolvedAuthDir != "" {
			authDir = resolvedAuthDir
		}
		if authDir != "" {
			authDir = filepath.Clean(authDir)
			if !filepath.IsAbs(authDir) {
				if abs, errAbs := filepath.Abs(authDir); errAbs == nil {
					authDir = abs
				}
			}
			if rel, errRel := filepath.Rel(authDir, path); errRel == nil && rel != "" {
				id = rel
			}
		}
	}
	// On Windows, normalize ID casing to avoid duplicate auth entries caused by case-insensitive paths.
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func (h *Handler) registerAuthFromFile(ctx context.Context, path string, data []byte) error {
	if h.authManager == nil {
		return nil
	}
	auths, err := h.buildAuthsFromFileData(path, data)
	if err != nil {
		return err
	}
	for _, auth := range auths {
		if err := h.upsertAuthRecord(ctx, auth); err != nil {
			return err
		}
	}
	return nil
}

// buildAuthsFromFileData synthesizes one or more auth records from an auth
// file payload. A Codex CLI native export (accounts[].credentials) expands into
// one auth per account; flat auth files yield a single auth. Each synthesized
// auth keeps the ID assigned by the synthesizer (relative path, or
// "<relpath>#<ordinal>" for multi-account bundles) so the watcher and the
// management panel see distinct records instead of one overwriting the other.
func (h *Handler) buildAuthsFromFileData(path string, data []byte) ([]*coreauth.Auth, error) {
	if path == "" {
		return nil, fmt.Errorf("auth path is empty")
	}
	if data == nil {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read auth file: %w", err)
		}
	}
	metadata := make(map[string]any)
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("invalid auth file: %w", err)
	}
	provider, _ := metadata["type"].(string)
	if provider == "" {
		provider = "unknown"
	}
	label := provider
	if email, ok := metadata["email"].(string); ok && email != "" {
		label = email
	}
	lastRefresh, hasLastRefresh := extractLastRefreshTimestamp(metadata)
	authID := h.authIDForPath(path)
	if authID == "" {
		authID = path
	}
	now := time.Now()
	var generated []*coreauth.Auth
	if h != nil && h.cfg != nil {
		sctx := &synthesizer.SynthesisContext{
			Config:      h.cfg,
			AuthDir:     h.cfg.AuthDir,
			Now:         now,
			IDGenerator: synthesizer.NewStableIDGenerator(),
		}
		generated = synthesizer.SynthesizeAuthFile(sctx, path, data)
	}
	if len(generated) == 0 {
		generated = []*coreauth.Auth{{
			ID:       authID,
			Provider: provider,
			Label:    label,
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path":   path,
				"source": path,
			},
			Metadata:  metadata,
			CreatedAt: now,
			UpdatedAt: now,
		}}
	}
	out := make([]*coreauth.Auth, 0, len(generated))
	for _, g := range generated {
		if g == nil {
			continue
		}
		auth := g.Clone()
		// Single-auth files keep the path-derived ID (identical to the
		// synthesizer's base ID). Multi-account bundles keep the synthesizer's
		// "<baseID>#<ordinal>" ID so each account is a distinct record.
		if len(generated) == 1 {
			auth.ID = authID
		}
		auth.FileName = filepath.Base(path)
		if hasLastRefresh {
			auth.LastRefreshedAt = lastRefresh
		}
		if h != nil && h.authManager != nil {
			if existing, ok := h.authManager.GetByID(auth.ID); ok {
				auth.CreatedAt = existing.CreatedAt
				if !hasLastRefresh {
					auth.LastRefreshedAt = existing.LastRefreshedAt
				}
				auth.NextRefreshAfter = existing.NextRefreshAfter
				auth.Runtime = existing.Runtime
			}
		}
		coreauth.ApplyCustomHeadersFromMetadata(auth)
		out = append(out, auth)
	}
	return out, nil
}

// buildAuthFromFileData returns the first synthesized auth record for an auth
// file. It is kept for call sites that only need provider/label metadata (e.g.
// codex account_id backfill checks). Use buildAuthsFromFileData when registering.
func (h *Handler) buildAuthFromFileData(path string, data []byte) (*coreauth.Auth, error) {
	auths, err := h.buildAuthsFromFileData(path, data)
	if err != nil {
		return nil, err
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[0], nil
}

func (h *Handler) upsertAuthRecord(ctx context.Context, auth *coreauth.Auth) error {
	if h == nil || h.authManager == nil || auth == nil {
		return nil
	}
	if existing, ok := h.authManager.GetByID(auth.ID); ok {
		auth.CreatedAt = existing.CreatedAt
		_, err := h.authManager.Update(ctx, auth)
		return err
	}
	_, err := h.authManager.Register(ctx, auth)
	return err
}

// PatchAuthFileStatus toggles the disabled state of an auth file
func (h *Handler) PatchAuthFileStatus(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name     string `json:"name"`
		Disabled *bool  `json:"disabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if req.Disabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "disabled is required"})
		return
	}

	ctx := c.Request.Context()

	// Find auth by name or ID
	var targetAuth *coreauth.Auth
	if auth, ok := h.authManager.GetByID(name); ok {
		targetAuth = auth
	} else {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name {
				targetAuth = auth
				break
			}
		}
	}

	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	if coreauth.IsPluginVirtualAuth(targetAuth) {
		// Allow status changes only when targeting the source auth file name, matching delete semantics.
		// Expanded virtual project auths still cannot be modified independently.
		if !isPluginVirtualSourceDelete(name, targetAuth) {
			c.JSON(http.StatusConflict, gin.H{"error": errPluginVirtualAuth.Error()})
			return
		}
		if errPatch := h.patchSourceAuthFileStatus(ctx, targetAuth, *req.Disabled); errPatch != nil {
			status := http.StatusInternalServerError
			if errors.Is(errPatch, errAuthFileNotFound) || os.IsNotExist(errPatch) {
				status = http.StatusNotFound
			}
			c.JSON(status, gin.H{"error": errPatch.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "disabled": *req.Disabled})
		return
	}
	if coreauth.IsFileBundleAuth(targetAuth) {
		if errPatch := h.patchSourceAuthFileStatus(ctx, targetAuth, *req.Disabled); errPatch != nil {
			status := http.StatusInternalServerError
			if errors.Is(errPatch, errAuthFileNotFound) || os.IsNotExist(errPatch) {
				status = http.StatusNotFound
			}
			c.JSON(status, gin.H{"error": errPatch.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "disabled": *req.Disabled})
		return
	}

	if coreauth.IsConfigAPIKeyAuth(targetAuth) {
		h.mu.Lock()
		handled, errToggle := toggleConfigAPIKeyExcludedAll(h.cfg, targetAuth, *req.Disabled)
		if errToggle != nil {
			h.mu.Unlock()
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update config api key: %v", errToggle)})
			return
		}
		if !handled {
			h.mu.Unlock()
			c.JSON(http.StatusNotFound, gin.H{"error": "config api key entry not found"})
			return
		}
		cfgSnapshot, okSnapshot := h.saveConfigAndSnapshotLocked(c)
		h.mu.Unlock()
		if !okSnapshot {
			return
		}
		h.reloadConfigAfterManagementSave(ctx, cfgSnapshot)
		if h.tokenStore != nil {
			_ = h.tokenStore.Delete(ctx, targetAuth.ID)
		}
		c.JSON(http.StatusOK, gin.H{
			"status":           "ok",
			"disabled":         *req.Disabled,
			"via":              "config:excluded-models",
			"excluded_pattern": configAPIKeyDisablePattern,
		})
		return
	}

	applyAuthDisabledState(targetAuth, *req.Disabled)
	if _, err := h.authManager.Update(ctx, targetAuth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "disabled": *req.Disabled})
}

// patchSourceAuthFileStatus toggles disabled on the source file and every
// runtime auth expanded from it, including plugin project auths and members of
// a multi-account auth bundle.
func (h *Handler) patchSourceAuthFileStatus(ctx context.Context, targetAuth *coreauth.Auth, disabled bool) error {
	if h == nil || h.authManager == nil || targetAuth == nil {
		return fmt.Errorf("core auth manager unavailable")
	}
	sourcePath := strings.TrimSpace(authAttribute(targetAuth, coreauth.AttributeVirtualSource))
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(authAttribute(targetAuth, "path"))
	}
	if sourcePath == "" {
		return fmt.Errorf("source auth path is empty")
	}
	if errWrite := setSourceAuthFileDisabled(sourcePath, disabled); errWrite != nil {
		if os.IsNotExist(errWrite) {
			return errAuthFileNotFound
		}
		return fmt.Errorf("failed to update source auth file: %w", errWrite)
	}
	now := time.Now()
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if !sameAuthFilePath(authAttribute(auth, "path"), sourcePath) &&
			!sameAuthFilePath(authAttribute(auth, coreauth.AttributeVirtualSource), sourcePath) {
			continue
		}
		applyAuthDisabledState(auth, disabled)
		auth.UpdatedAt = now
		if _, errUpdate := h.authManager.Update(ctx, auth); errUpdate != nil {
			return fmt.Errorf("failed to update auth %s: %w", auth.ID, errUpdate)
		}
	}
	return nil
}

func setSourceAuthFileDisabled(path string, disabled bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("source auth path is empty")
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return errRead
	}
	metadata := make(map[string]any)
	if len(bytes.TrimSpace(data)) > 0 {
		if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
			return fmt.Errorf("invalid auth file: %w", errUnmarshal)
		}
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata["disabled"] = disabled
	raw, errMarshal := json.Marshal(metadata)
	if errMarshal != nil {
		return fmt.Errorf("marshal auth file: %w", errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		return errWrite
	}
	return nil
}

func applyAuthDisabledState(auth *coreauth.Auth, disabled bool) {
	if auth == nil {
		return
	}
	auth.Disabled = disabled
	if disabled {
		auth.Status = coreauth.StatusDisabled
		auth.StatusMessage = "disabled via management API"
	} else {
		auth.Status = coreauth.StatusActive
		auth.StatusMessage = ""
	}
	auth.UpdatedAt = time.Now()
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["disabled"] = disabled
}

// PatchAuthFileFields updates arbitrary metadata fields of an auth file.
func (h *Handler) PatchAuthFileFields(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req map[string]json.RawMessage
	decoder := json.NewDecoder(c.Request.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	nameRaw, ok := req["name"]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var nameValue string
	if err := json.Unmarshal(nameRaw, &nameValue); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	name := strings.TrimSpace(nameValue)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	delete(req, "name")

	ctx := c.Request.Context()

	// Find auth by name or ID
	var targetAuth *coreauth.Auth
	if auth, ok := h.authManager.GetByID(name); ok {
		targetAuth = auth
	} else {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name {
				targetAuth = auth
				break
			}
		}
	}

	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	if coreauth.IsPluginVirtualAuth(targetAuth) {
		c.JSON(http.StatusConflict, gin.H{"error": errPluginVirtualAuth.Error()})
		return
	}
	if coreauth.IsFileBundleAuth(targetAuth) {
		c.JSON(http.StatusConflict, gin.H{"error": errFileBundleAuth.Error()})
		return
	}

	changed := false
	touchedRoots := make(map[string]struct{}, len(req))
	for key, rawValue := range req {
		fieldPath := strings.TrimSpace(key)
		if fieldPath == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "field name is required"})
			return
		}
		value, errDecode := decodeAuthFileFieldValue(rawValue)
		if errDecode != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid field %s", fieldPath)})
			return
		}
		if targetAuth.Metadata == nil {
			targetAuth.Metadata = make(map[string]any)
		}

		if fieldPath == "headers" {
			applyAuthFileHeadersPatch(targetAuth, value)
		} else if normalized, okNormalize, errNormalize := normalizeAuthFileTagPatchValue(fieldPath, value); errNormalize != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errNormalize.Error()})
			return
		} else if okNormalize {
			targetAuth.Metadata[fieldPath] = normalized
		} else if errSet := setAuthFileMetadataValue(targetAuth.Metadata, fieldPath, value); errSet != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errSet.Error()})
			return
		}
		if root := rootAuthFileField(fieldPath); root != "" {
			touchedRoots[root] = struct{}{}
		}
		changed = true
	}
	if changed {
		syncAuthFileMetadataFields(targetAuth, touchedRoots)
	}

	if !changed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	targetAuth.UpdatedAt = time.Now()

	if _, err := h.authManager.Update(ctx, targetAuth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func decodeAuthFileFieldValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func rootAuthFileField(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if idx := strings.Index(path, "."); idx >= 0 {
		return strings.TrimSpace(path[:idx])
	}
	return path
}

func setAuthFileMetadataValue(metadata map[string]any, path string, value any) error {
	if metadata == nil {
		return fmt.Errorf("metadata is nil")
	}
	parts := strings.Split(path, ".")
	current := metadata
	for i, rawPart := range parts {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			return fmt.Errorf("invalid field path: %s", path)
		}
		if i == len(parts)-1 {
			current[part] = value
			return nil
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[part] = next
		}
		current = next
	}
	return nil
}

func applyAuthFileHeadersPatch(auth *coreauth.Auth, value any) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	headersPatch, ok := authFileHeadersStringMap(value)
	if !ok {
		auth.Metadata["headers"] = value
		return
	}

	existingHeaders := coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata)
	nextHeaders := make(map[string]string, len(existingHeaders))
	for key, val := range existingHeaders {
		nextHeaders[key] = val
	}
	for key, value := range headersPatch {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		val := strings.TrimSpace(value)
		if val == "" {
			delete(nextHeaders, name)
			continue
		}
		nextHeaders[name] = val
	}

	if len(nextHeaders) == 0 {
		delete(auth.Metadata, "headers")
		return
	}
	metaHeaders := make(map[string]any, len(nextHeaders))
	for key, value := range nextHeaders {
		metaHeaders[key] = value
	}
	auth.Metadata["headers"] = metaHeaders
}

func authFileHeadersStringMap(value any) (map[string]string, bool) {
	switch typed := value.(type) {
	case map[string]string:
		return typed, true
	case map[string]any:
		out := make(map[string]string, len(typed))
		for key, rawValue := range typed {
			value, ok := rawValue.(string)
			if !ok {
				return nil, false
			}
			out[key] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func syncAuthFileMetadataFields(auth *coreauth.Auth, touchedRoots map[string]struct{}) {
	if auth == nil || len(touchedRoots) == 0 {
		return
	}
	if _, ok := touchedRoots["prefix"]; ok {
		if prefix, okString := auth.Metadata["prefix"].(string); okString {
			auth.Prefix = strings.TrimSpace(prefix)
		}
	}
	if _, ok := touchedRoots["proxy_url"]; ok {
		if proxyURL, okString := auth.Metadata["proxy_url"].(string); okString {
			auth.ProxyURL = strings.TrimSpace(proxyURL)
		}
	}
	if _, ok := touchedRoots["headers"]; ok {
		syncAuthFileHeaderAttributes(auth)
	}
	if _, ok := touchedRoots["label"]; ok {
		syncAuthFileLabel(auth)
	}
	if _, ok := touchedRoots["priority"]; ok {
		syncAuthFilePriorityAttribute(auth)
	}
	if _, ok := touchedRoots["note"]; ok {
		syncAuthFileNoteAttribute(auth)
	}
	if _, ok := touchedRoots["websockets"]; ok {
		syncAuthFileWebsocketsAttribute(auth)
	}
	if _, ok := touchedRoots["codex_fast_mode"]; ok {
		syncAuthFileCodexFastModeAttribute(auth)
	}
	if _, ok := touchedRoots["disabled"]; ok {
		syncAuthFileDisabledState(auth)
	}
}

func normalizeAuthFileTagPatchValue(fieldPath string, value any) ([]string, bool, error) {
	switch fieldPath {
	case "custom_tags":
		values, ok := authFileStringSlice(value)
		if !ok {
			return nil, true, fmt.Errorf("custom_tags must be an array of strings")
		}
		normalized, err := normalizeEditableTags(values, maxCustomAuthTags)
		if err != nil {
			return nil, true, err
		}
		return normalized, true, nil
	case "hidden_default_tags", "display_tags":
		values, ok := authFileStringSlice(value)
		if !ok {
			return nil, true, fmt.Errorf("%s must be an array of strings", fieldPath)
		}
		return normalizeTagList(values), true, nil
	default:
		return nil, false, nil
	}
}

func authFileStringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	default:
		return nil, false
	}
}

func syncAuthFileLabel(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	label, ok := auth.Metadata["label"].(string)
	if !ok {
		auth.Label = ""
		return
	}
	auth.Label = strings.TrimSpace(label)
}

func syncAuthFileHeaderAttributes(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	for key := range auth.Attributes {
		if strings.HasPrefix(key, "header:") {
			delete(auth.Attributes, key)
		}
	}
	for name, value := range coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata) {
		auth.Attributes["header:"+name] = value
	}
}

func syncAuthFilePriorityAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	priority, ok := authFileIntValue(auth.Metadata["priority"])
	if !ok {
		delete(auth.Attributes, "priority")
		return
	}
	if priority == 0 {
		delete(auth.Attributes, "priority")
		return
	}
	auth.Attributes["priority"] = strconv.Itoa(priority)
}

func authFileIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return int(i), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return i, true
		}
	}
	return 0, false
}

func syncAuthFileNoteAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	note, ok := auth.Metadata["note"].(string)
	if !ok {
		delete(auth.Attributes, "note")
		return
	}
	note = strings.TrimSpace(note)
	if note == "" {
		delete(auth.Attributes, "note")
		return
	}
	auth.Attributes["note"] = note
}

func syncAuthFileWebsocketsAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	websockets, ok := authFileBoolValue(auth.Metadata["websockets"])
	if !ok {
		delete(auth.Attributes, "websockets")
		return
	}
	auth.Attributes["websockets"] = strconv.FormatBool(websockets)
}

func syncAuthFileCodexFastModeAttribute(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	fastMode, ok := authFileBoolValue(auth.Metadata["codex_fast_mode"])
	if !ok {
		delete(auth.Attributes, "codex_fast_mode")
		return
	}
	auth.Attributes["codex_fast_mode"] = strconv.FormatBool(fastMode)
}

func authFileBoolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(typed))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func syncAuthFileDisabledState(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	disabled, ok := authFileBoolValue(auth.Metadata["disabled"])
	if !ok {
		return
	}
	auth.Disabled = disabled
	if disabled {
		auth.Status = coreauth.StatusDisabled
		if strings.TrimSpace(auth.StatusMessage) == "" {
			auth.StatusMessage = "disabled via management API"
		}
		return
	}
	auth.Status = coreauth.StatusActive
	auth.StatusMessage = ""
}

func (h *Handler) removeAuth(ctx context.Context, id string) {
	if h == nil || h.authManager == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	if _, ok := h.authManager.GetByID(id); ok {
		h.authManager.Remove(ctx, id)
		return
	}
	authID := h.authIDForPath(id)
	if authID == "" {
		return
	}
	h.authManager.Remove(ctx, authID)
}

func (h *Handler) removeAuthsForPath(ctx context.Context, path string, fallbackID string) {
	if h == nil || h.authManager == nil {
		return
	}
	removed := false
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if sameAuthFilePath(authAttribute(auth, "path"), path) || sameAuthFilePath(authAttribute(auth, coreauth.AttributeVirtualSource), path) {
			h.removeAuth(ctx, auth.ID)
			removed = true
		}
	}
	if removed {
		return
	}
	if strings.TrimSpace(fallbackID) != "" {
		h.removeAuth(ctx, fallbackID)
		return
	}
	h.removeAuth(ctx, path)
}

func sameAuthFilePath(left, right string) bool {
	left = cleanAuthFilePath(left)
	right = cleanAuthFilePath(right)
	if left == "" || right == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func cleanAuthFilePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, errAbs := filepath.Abs(path); errAbs == nil && strings.TrimSpace(abs) != "" {
		path = abs
	}
	return filepath.Clean(path)
}

func (h *Handler) deleteTokenRecord(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("auth path is empty")
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return fmt.Errorf("token store unavailable")
	}
	return store.Delete(ctx, path)
}

func (h *Handler) tokenStoreWithBaseDir() coreauth.Store {
	if h == nil {
		return nil
	}
	store := h.tokenStore
	if store == nil {
		store = sdkAuth.GetTokenStore()
		h.tokenStore = store
	}
	if h.cfg != nil {
		if dirSetter, ok := store.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(h.cfg.AuthDir)
		}
	}
	return store
}

func (h *Handler) saveTokenRecord(ctx context.Context, record *coreauth.Auth) (string, error) {
	if record == nil {
		return "", fmt.Errorf("token record is nil")
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return "", fmt.Errorf("token store unavailable")
	}
	if h.postAuthHook != nil {
		if err := h.postAuthHook(ctx, record); err != nil {
			return "", fmt.Errorf("post-auth hook failed: %w", err)
		}
	}
	savedPath, errSave := store.Save(ctx, record)
	if errSave != nil {
		return savedPath, errSave
	}
	if h.postAuthPersistHook != nil {
		if errHook := h.postAuthPersistHook(ctx, record); errHook != nil {
			return savedPath, fmt.Errorf("post-auth persist hook failed: %w", errHook)
		}
	}
	return savedPath, nil
}

func (h *Handler) RequestAnthropicToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Claude authentication...")

	// Generate PKCE codes
	pkceCodes, err := claude.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	// Initialize Claude auth service
	anthropicAuth := claude.NewClaudeAuth(h.cfg)

	// Generate authorization URL (then override redirect_uri to reuse server port)
	authURL, state, err := anthropicAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "anthropic")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/anthropic/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute anthropic callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(anthropicCallbackPort, "anthropic", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start anthropic callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(anthropicCallbackPort, forwarder)
		}

		// Helper: wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-anthropic-%s.oauth", state))
		waitForFile := func(path string, timeout time.Duration) (map[string]string, error) {
			deadline := time.Now().Add(timeout)
			for {
				if !IsOAuthSessionPending(state, "anthropic") {
					return nil, errOAuthSessionNotPending
				}
				if time.Now().After(deadline) {
					SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
					return nil, fmt.Errorf("timeout waiting for OAuth callback")
				}
				data, errRead := os.ReadFile(path)
				if errRead == nil {
					var m map[string]string
					_ = json.Unmarshal(data, &m)
					_ = os.Remove(path)
					return m, nil
				}
				time.Sleep(500 * time.Millisecond)
			}
		}

		fmt.Println("Waiting for authentication callback...")
		// Wait up to 5 minutes
		resultMap, errWait := waitForFile(waitFile, 5*time.Minute)
		if errWait != nil {
			if errors.Is(errWait, errOAuthSessionNotPending) {
				return
			}
			authErr := claude.NewAuthenticationError(claude.ErrCallbackTimeout, errWait)
			log.Error(claude.GetUserFriendlyMessage(authErr))
			return
		}
		if errStr := resultMap["error"]; errStr != "" {
			oauthErr := claude.NewOAuthError(errStr, "", http.StatusBadRequest)
			log.Error(claude.GetUserFriendlyMessage(oauthErr))
			SetOAuthSessionError(state, "Bad request")
			return
		}
		if resultMap["state"] != state {
			authErr := claude.NewAuthenticationError(claude.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, resultMap["state"]))
			log.Error(claude.GetUserFriendlyMessage(authErr))
			SetOAuthSessionError(state, "State code error")
			return
		}

		// Parse code (Claude may append state after '#')
		rawCode := resultMap["code"]
		code := strings.Split(rawCode, "#")[0]

		// Exchange code for tokens using internal auth service
		bundle, errExchange := anthropicAuth.ExchangeCodeForTokens(ctx, code, state, pkceCodes)
		if errExchange != nil {
			authErr := claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, errExchange)
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			SetOAuthSessionError(state, "Failed to exchange authorization code for tokens")
			return
		}

		// Create token storage
		tokenStorage := anthropicAuth.CreateTokenStorage(bundle)
		record := &coreauth.Auth{
			ID:       fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Provider: "claude",
			FileName: fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Storage:  tokenStorage,
			Metadata: map[string]any{"email": tokenStorage.Email},
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "anthropic"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Claude services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestCodexToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)
	egressID := strings.TrimSpace(c.Query("egress_id"))
	if egressID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "egress_required", "message": "egress_id is required for Codex OAuth"}})
		return
	}
	egressService, oauthHTTPClient, err := h.codexOAuthEgressClient(ctx, egressID)
	if err != nil {
		writeEgressError(c, err)
		return
	}
	if err = codexOAuthEndpointAvailable(ctx, egressService, egressID); err != nil {
		writeEgressError(c, err)
		return
	}

	fmt.Println("Initializing Codex authentication...")

	// Generate PKCE codes
	pkceCodes, err := codex.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}
	if err = RegisterOAuthSessionWithEndpointReservation(state, "codex", egressID, map[string]any{"egress_id": egressID}); err != nil {
		writeEgressError(c, fmt.Errorf("%w: %v", egress.ErrEndpointInUse, err))
		return
	}

	// Initialize Codex auth service
	oauthCfg := h.codexOAuthConfig()
	openaiAuth := newCodexOAuthService(oauthCfg, oauthHTTPClient)
	authDir := ""
	if oauthCfg != nil {
		authDir = oauthCfg.AuthDir
	}

	// Generate authorization URL
	authURL, err := openaiAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		CompleteOAuthSession(state)
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/codex/callback")
		if errTarget != nil {
			CompleteOAuthSession(state)
			log.WithError(errTarget).Error("failed to compute codex callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(codexCallbackPort, "codex", targetURL); errStart != nil {
			CompleteOAuthSession(state)
			log.WithError(errStart).Error("failed to start codex callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(codexCallbackPort, forwarder)
		}

		// Wait for callback file
		waitFile := filepath.Join(authDir, fmt.Sprintf(".oauth-codex-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var code string
		for {
			if !IsOAuthSessionPending(state, "codex") {
				return
			}
			if time.Now().After(deadline) {
				authErr := codex.NewAuthenticationError(codex.ErrCallbackTimeout, fmt.Errorf("timeout waiting for OAuth callback"))
				log.Error(codex.GetUserFriendlyMessage(authErr))
				SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				var m map[string]string
				_ = json.Unmarshal(data, &m)
				_ = os.Remove(waitFile)
				if errStr := m["error"]; errStr != "" {
					oauthErr := codex.NewOAuthError(errStr, "", http.StatusBadRequest)
					log.Error(codex.GetUserFriendlyMessage(oauthErr))
					SetOAuthSessionError(state, "Bad Request")
					return
				}
				if m["state"] != state {
					authErr := codex.NewAuthenticationError(codex.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, m["state"]))
					SetOAuthSessionError(state, "State code error")
					log.Error(codex.GetUserFriendlyMessage(authErr))
					return
				}
				code = m["code"]
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		log.Debug("Authorization code received, exchanging for tokens...")
		// Re-resolve the selected endpoint immediately before exchanging the
		// authorization code. Endpoint health or route changes must never leave
		// an OAuth session using an obsolete route.
		currentEgressService, exchangeHTTPClient, errRoute := h.codexOAuthEgressClient(ctx, egressID)
		if errRoute != nil {
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Codex OAuth egress is not ready", errRoute))
			log.Errorf("Codex OAuth egress recheck failed: %v", errRoute)
			return
		}
		if errAvailable := codexOAuthEndpointAvailable(ctx, currentEgressService, egressID); errAvailable != nil {
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Codex OAuth egress endpoint is no longer available", errAvailable))
			log.Errorf("Codex OAuth endpoint availability recheck failed: %v", errAvailable)
			return
		}
		exchangeAuth := newCodexOAuthService(h.codexOAuthConfig(), exchangeHTTPClient)
		// Exchange code for tokens using internal auth service
		bundle, errExchange := exchangeAuth.ExchangeCodeForTokens(ctx, code, pkceCodes)
		if errExchange != nil {
			authErr := codex.NewAuthenticationError(codex.ErrCodeExchangeFailed, errExchange)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Failed to exchange authorization code for tokens", errExchange))
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			return
		}

		// Extract additional info for filename generation
		claims, _ := codex.ParseJWTToken(bundle.TokenData.IDToken)
		planType := ""
		hashAccountID := ""
		if claims != nil {
			planType = strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
			if accountID := claims.GetAccountID(); accountID != "" {
				digest := sha256.Sum256([]byte(accountID))
				hashAccountID = hex.EncodeToString(digest[:])[:8]
			}
		}

		// Create token storage and persist
		tokenStorage := exchangeAuth.CreateTokenStorage(bundle)
		fileName := codex.CredentialFileName(tokenStorage.Email, planType, hashAccountID, true)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "codex",
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: map[string]any{
				"email":      tokenStorage.Email,
				"account_id": tokenStorage.AccountID,
			},
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "codex"); errGuard != nil {
			return
		}
		tokenStorage.SetMetadata(record.Metadata)
		savedPath, errSave := h.saveCodexTokenWithBinding(ctx, currentEgressService, egressID, record, tokenStorage)
		if errSave != nil {
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			return
		}
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Codex services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) codexOAuthEgressClient(ctx context.Context, egressID string) (*egress.Service, *http.Client, error) {
	if !h.egressRuntimeEnabled() {
		return nil, nil, fmt.Errorf("%w: egress runtime is disabled", egress.ErrEgressRequired)
	}
	service := h.egress()
	if service == nil {
		return nil, nil, fmt.Errorf("%w: egress service is unavailable", egress.ErrEgressRequired)
	}
	readiness, err := service.EndpointReadiness(ctx, egressID)
	if err != nil {
		return nil, nil, err
	}
	if !readiness.RuntimeReady {
		return nil, nil, fmt.Errorf("%w: endpoint %s is not ready: %s", egress.ErrEndpointDisabled, egressID, strings.Join(readiness.Reasons, ","))
	}
	client, err := service.HTTPClient(ctx, egressID, 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	return service, client, nil
}

func codexOAuthEndpointAvailable(ctx context.Context, service *egress.Service, egressID string) error {
	if service == nil {
		return fmt.Errorf("%w: egress service is unavailable", egress.ErrEgressRequired)
	}
	// Shared endpoints are designed to host multiple identities, so an existing
	// binding is not a conflict there. Only exclusive endpoints must be free
	// before an OAuth login claims them.
	if endpoint, err := service.GetEndpoint(ctx, egressID); err == nil &&
		endpoint.SharingMode == egress.EndpointSharingModeShared {
		return nil
	}
	impact, err := service.EndpointImpact(ctx, egressID, egress.EndpointActionDisable)
	if err != nil {
		return err
	}
	if impact.BindingCount > 0 {
		return fmt.Errorf("%w: endpoint %s already has %d binding(s)", egress.ErrEndpointInUse, egressID, impact.BindingCount)
	}
	return nil
}

func (h *Handler) codexOAuthConfig() *config.Config {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg
}

func (h *Handler) authManagerSnapshot() *coreauth.Manager {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.authManager
}

func (h *Handler) saveCodexTokenWithBinding(ctx context.Context, service *egress.Service, egressID string, record *coreauth.Auth, storage *codex.CodexTokenStorage) (string, error) {
	if service == nil || record == nil || storage == nil {
		return "", fmt.Errorf("Codex token binding inputs are incomplete")
	}
	// The binding database is the only durable source of the selected endpoint.
	// Strip legacy copies before persistence; resolveEgressAuth injects the current
	// endpoint into a runtime clone when a request is executed.
	delete(record.Metadata, "egress_id")
	delete(record.Attributes, "egress_id")
	storage.SetMetadata(record.Metadata)
	identity, err := egress.StableIdentity(storage.AccountID)
	if err != nil {
		return "", fmt.Errorf("derive Codex egress identity: %w", err)
	}
	targetPath := record.FileName
	cfg := h.codexOAuthConfig()
	if !filepath.IsAbs(targetPath) && cfg != nil {
		targetPath = filepath.Join(cfg.AuthDir, targetPath)
	}
	targetPath = cleanAuthFilePath(targetPath)
	pathLockKey := targetPath
	if pathLockKey == "" {
		pathLockKey = strings.TrimSpace(record.ID)
	}
	if runtime.GOOS == "windows" {
		pathLockKey = strings.ToLower(pathLockKey)
	}
	unlock := codexTokenBindingLocks.lock("path:"+pathLockKey, "identity:"+identity)
	defer unlock()

	previousBytes, readErr := os.ReadFile(targetPath)
	previousExists := readErr == nil
	var previousMode os.FileMode = 0o600
	if previousExists {
		if info, errStat := os.Stat(targetPath); errStat == nil {
			previousMode = info.Mode().Perm()
		}
	}
	var previousRuntime *coreauth.Auth
	manager := h.authManagerSnapshot()
	if manager != nil {
		if existing, ok := manager.GetByID(record.ID); ok && existing != nil {
			previousRuntime = existing.Clone()
		}
	}

	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		return savedPath, err
	}
	err = service.PutBinding(ctx, egress.Binding{Identity: identity, EndpointID: egressID, AuthFileID: record.ID})
	if err == nil {
		return savedPath, nil
	}

	rollbackCtx := coreauth.WithSkipPersist(context.WithoutCancel(ctx))
	if previousExists {
		if errRestore := os.WriteFile(targetPath, previousBytes, previousMode); errRestore != nil {
			return savedPath, fmt.Errorf("bind Codex egress: %w; restore previous token: %v", err, errRestore)
		}
		if manager != nil && previousRuntime != nil {
			if _, errUpdate := manager.Update(rollbackCtx, previousRuntime); errUpdate != nil {
				return savedPath, fmt.Errorf("bind Codex egress: %w; restore previous runtime: %v", err, errUpdate)
			}
		} else if manager != nil {
			manager.Remove(rollbackCtx, record.ID)
		}
	} else {
		if errDelete := h.deleteTokenRecord(rollbackCtx, savedPath); errDelete != nil {
			return savedPath, fmt.Errorf("bind Codex egress: %w; delete new token: %v", err, errDelete)
		}
		if manager != nil {
			manager.Remove(rollbackCtx, record.ID)
		}
	}
	return savedPath, fmt.Errorf("bind Codex egress endpoint: %w", err)
}

func (h *Handler) RequestAntigravityToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Antigravity authentication...")

	authSvc := antigravity.NewAntigravityAuth(h.cfg, nil)

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate state parameter: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", antigravity.CallbackPort)
	authURL := authSvc.BuildAuthURL(state, redirectURI)

	RegisterOAuthSession(state, "antigravity")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/antigravity/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute antigravity callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(antigravity.CallbackPort, "antigravity", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start antigravity callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(antigravity.CallbackPort, forwarder)
		}

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-antigravity-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var authCode string
		for {
			if !IsOAuthSessionPending(state, "antigravity") {
				return
			}
			if time.Now().After(deadline) {
				log.Error("oauth flow timed out")
				SetOAuthSessionError(state, "OAuth flow timed out")
				return
			}
			if data, errReadFile := os.ReadFile(waitFile); errReadFile == nil {
				var payload map[string]string
				_ = json.Unmarshal(data, &payload)
				_ = os.Remove(waitFile)
				if errStr := strings.TrimSpace(payload["error"]); errStr != "" {
					log.Errorf("Authentication failed: %s", errStr)
					SetOAuthSessionError(state, "Authentication failed")
					return
				}
				if payloadState := strings.TrimSpace(payload["state"]); payloadState != "" && payloadState != state {
					log.Errorf("Authentication failed: state mismatch")
					SetOAuthSessionError(state, "Authentication failed: state mismatch")
					return
				}
				authCode = strings.TrimSpace(payload["code"])
				if authCode == "" {
					log.Error("Authentication failed: code not found")
					SetOAuthSessionError(state, "Authentication failed: code not found")
					return
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		tokenResp, errToken := authSvc.ExchangeCodeForTokens(ctx, authCode, redirectURI)
		if errToken != nil {
			log.Errorf("Failed to exchange token: %v", errToken)
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		accessToken := strings.TrimSpace(tokenResp.AccessToken)
		if accessToken == "" {
			log.Error("antigravity: token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		email, errInfo := authSvc.FetchUserInfo(ctx, accessToken)
		if errInfo != nil {
			log.Errorf("Failed to fetch user info: %v", errInfo)
			SetOAuthSessionError(state, "Failed to fetch user info")
			return
		}
		email = strings.TrimSpace(email)
		if email == "" {
			log.Error("antigravity: user info returned empty email")
			SetOAuthSessionError(state, "Failed to fetch user info")
			return
		}

		projectID := ""
		if accessToken != "" {
			fetchedProjectID, errProject := authSvc.FetchProjectID(ctx, accessToken)
			if errProject != nil {
				log.Warnf("antigravity: failed to fetch project ID: %v", errProject)
			} else {
				projectID = fetchedProjectID
				log.Infof("antigravity: obtained project ID %s", util.HideAPIKey(projectID))
			}
		}

		now := time.Now()
		metadata := map[string]any{
			"type":          "antigravity",
			"access_token":  tokenResp.AccessToken,
			"refresh_token": tokenResp.RefreshToken,
			"expires_in":    tokenResp.ExpiresIn,
			"timestamp":     now.UnixMilli(),
			"expired":       now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
		}
		if email != "" {
			metadata["email"] = email
		}
		if projectID != "" {
			metadata["project_id"] = projectID
		}

		fileName := antigravity.CredentialFileName(email)
		label := strings.TrimSpace(email)
		if label == "" {
			label = "antigravity"
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "antigravity",
			FileName: fileName,
			Label:    label,
			Metadata: metadata,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "antigravity"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if projectID != "" {
			fmt.Printf("Using GCP project: %s\n", util.HideAPIKey(projectID))
		}
		fmt.Println("You can now use Antigravity services through this CLI")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestXAIToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)
	usingAPI := false
	if parsed, errParse := strconv.ParseBool(strings.TrimSpace(c.Query("using_api"))); errParse == nil {
		usingAPI = parsed
	}

	fmt.Println("Initializing xAI authentication...")

	state := fmt.Sprintf("xai-%d", time.Now().UnixNano())
	authSvc := xaiauth.NewXAIAuth(h.cfg)

	deviceFlow, errStartDeviceFlow := authSvc.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to start xAI device flow: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(deviceFlow.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(deviceFlow.VerificationURI)
	}

	RegisterOAuthSession(state, "xai")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "xai")

		fmt.Println("Waiting for xAI authentication...")
		bundle, errWaitForAuthorization := authSvc.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "xai") {
				return
			}
			log.Errorf("xAI authentication failed: %v", errWaitForAuthorization)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization))
			return
		}
		if !IsOAuthSessionPending(state, "xai") {
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(bundle)
		if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
			log.Error("xAI token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		record := xaiauth.OAuthRecordFromTokenStorage(tokenStorage, usingAPI)
		if record == nil {
			log.Error("xAI token exchange returned an invalid auth record")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "xai"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save xAI token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use xAI services through this CLI")
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	} else {
		response["expires_in"] = int(xaiauth.MaxPollDuration / time.Second)
	}
	c.JSON(200, response)
}

func (h *Handler) RequestKimiToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Kimi authentication...")

	state := fmt.Sprintf("kmi-%d", time.Now().UnixNano())
	// Initialize Kimi auth service
	kimiAuth := kimi.NewKimiAuth(h.cfg)

	// Generate authorization URL
	deviceFlow, errStartDeviceFlow := kimiAuth.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to generate authorization URL: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}
	authURL := deviceFlow.VerificationURIComplete
	if authURL == "" {
		authURL = deviceFlow.VerificationURI
	}

	RegisterOAuthSession(state, "kimi")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "kimi")

		fmt.Println("Waiting for authentication...")
		authBundle, errWaitForAuthorization := kimiAuth.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "kimi") {
				return
			}
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization))
			fmt.Printf("Authentication failed: %v\n", errWaitForAuthorization)
			return
		}
		if !IsOAuthSessionPending(state, "kimi") {
			return
		}

		// Create token storage
		tokenStorage := kimiAuth.CreateTokenStorage(authBundle)

		metadata := map[string]any{
			"type":          "kimi",
			"access_token":  authBundle.TokenData.AccessToken,
			"refresh_token": authBundle.TokenData.RefreshToken,
			"token_type":    authBundle.TokenData.TokenType,
			"scope":         authBundle.TokenData.Scope,
			"timestamp":     time.Now().UnixMilli(),
		}
		if authBundle.TokenData.ExpiresAt > 0 {
			expired := time.Unix(authBundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
			metadata["expired"] = expired
		}
		if strings.TrimSpace(authBundle.DeviceID) != "" {
			metadata["device_id"] = strings.TrimSpace(authBundle.DeviceID)
		}

		fileName := fmt.Sprintf("kimi-%d.json", time.Now().UnixMilli())
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kimi",
			FileName: fileName,
			Label:    "Kimi User",
			Storage:  tokenStorage,
			Metadata: metadata,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "kimi"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Kimi services through this CLI")
		CompleteOAuthSession(state)
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	}
	c.JSON(200, response)
}

// watchOAuthSessionCancel cancels pollCtx once the OAuth session is no longer pending.
func watchOAuthSessionCancel(pollCtx context.Context, cancel context.CancelFunc, state, provider string) {
	if cancel == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-pollCtx.Done():
			return
		case <-ticker.C:
			if !IsOAuthSessionPending(state, provider) {
				cancel()
				return
			}
		}
	}
}

// CancelAuthSession cancels a pending OAuth session identified by state.
// Protected by management auth. Safe for both callback and device-code flows:
// waiters check IsOAuthSessionPending and exit without saving credentials.
func (h *Handler) CancelAuthSession(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "missing state"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}
	cancelled := CancelOAuthSession(state)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "cancelled": cancelled})
}

func (h *Handler) GetAuthStatus(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}

	provider, status, isPlugin, metadata, completed, ok := GetOAuthSessionDetails(state)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if completed {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if status != "" {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": status})
		return
	}
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if isPlugin && host != nil && host.HasAuthProvider(provider) {
		ctx := PopulateAuthContext(context.Background(), c)
		resp, handled, errPoll := host.PollLogin(ctx, provider, state, metadata)
		if handled {
			if errPoll != nil {
				message := strings.TrimSpace(errPoll.Error())
				if message == "" {
					message = "Authentication failed"
				}
				SetOAuthSessionError(state, message)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": message})
				return
			}
			switch resp.Status {
			case "", pluginapi.AuthLoginStatusPending:
				c.JSON(http.StatusOK, gin.H{"status": "wait"})
				return
			case pluginapi.AuthLoginStatusError:
				message := strings.TrimSpace(resp.Message)
				if message == "" {
					message = "Authentication failed"
				}
				SetOAuthSessionError(state, message)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": message})
				return
			case pluginapi.AuthLoginStatusSuccess:
				records := pluginLoginPollAuths(host, resp)
				if len(records) == 0 {
					SetOAuthSessionError(state, "Authentication failed")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Authentication failed"})
					return
				}
				if errSave := h.savePluginLoginRecords(ctx, records); errSave != nil {
					log.WithError(errSave).WithField("provider", provider).Error("failed to save plugin auth tokens")
					SetOAuthSessionError(state, "Failed to save authentication tokens")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Failed to save authentication tokens"})
					return
				}
				CompleteOAuthSession(state)
				c.JSON(http.StatusOK, gin.H{"status": "ok"})
				return
			default:
				c.JSON(http.StatusOK, gin.H{"status": "wait"})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "wait"})
}

func pluginLoginPollAuths(host *pluginhost.Host, resp pluginapi.AuthLoginPollResponse) []*coreauth.Auth {
	if host == nil {
		return nil
	}
	authDatas := resp.Auths
	if len(authDatas) == 0 {
		authDatas = []pluginapi.AuthData{resp.Auth}
	}
	records := make([]*coreauth.Auth, 0, len(authDatas))
	for _, authData := range authDatas {
		record := host.AuthDataToCoreAuth(authData, "", "")
		if record == nil {
			return nil
		}
		records = append(records, record)
	}
	return records
}

func (h *Handler) savePluginLoginRecords(ctx context.Context, records []*coreauth.Auth) error {
	savedPaths := make([]string, 0, len(records))
	for _, record := range records {
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if strings.TrimSpace(savedPath) != "" {
			savedPaths = append(savedPaths, savedPath)
		}
		if errSave != nil {
			h.rollbackSavedTokenRecords(ctx, savedPaths)
			return errSave
		}
	}
	return nil
}

func (h *Handler) rollbackSavedTokenRecords(ctx context.Context, savedPaths []string) {
	for i := len(savedPaths) - 1; i >= 0; i-- {
		path := strings.TrimSpace(savedPaths[i])
		if path == "" {
			continue
		}
		if errDelete := h.deleteTokenRecord(ctx, path); errDelete != nil {
			log.WithError(errDelete).WithField("path", path).Warn("failed to roll back plugin auth token")
		}
		h.removeAuthsForPath(ctx, path, path)
	}
}

// PopulateAuthContext extracts request info and adds it to the context
func PopulateAuthContext(ctx context.Context, c *gin.Context) context.Context {
	info := &coreauth.RequestInfo{
		Query:   c.Request.URL.Query(),
		Headers: c.Request.Header,
	}
	return coreauth.WithRequestInfo(ctx, info)
}
