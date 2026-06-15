// Package management provides the management API handlers and middleware
// for configuring the server and managing auth files.
package management

import (
	"context"
<<<<<<< HEAD
	"crypto/sha256"
=======
>>>>>>> upstream/main
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

type attemptInfo struct {
	count         int
	blockedUntil  time.Time
	lastActivity  time.Time // track last activity for cleanup
	lastFailureAt time.Time // dedup concurrent failures within a short window
}

// attemptCleanupInterval controls how often stale IP entries are purged
const attemptCleanupInterval = 1 * time.Hour

// attemptMaxIdleTime controls how long an IP can be idle before cleanup
const attemptMaxIdleTime = 2 * time.Hour

// Handler aggregates config reference, persistence path and helpers.
type Handler struct {
	cfg                    *config.Config
	configFilePath         string
	mu                     sync.Mutex
	attemptsMu             sync.Mutex
	failedAttempts         map[string]*attemptInfo // keyed by client IP
	authManager            *coreauth.Manager
	tokenStore             coreauth.Store
	localPassword          string
	allowRemoteOverride    bool
	envSecret              string
	logDir                 string
	postAuthHook           coreauth.PostAuthHook
	postAuthPersistHook    coreauth.PostAuthHook
	pluginHost             *pluginhost.Host
	configReloadHook       func(context.Context, *config.Config)
	pluginStoreRegistryURL string
	pluginStoreHTTPClient  pluginstore.HTTPDoer
	pluginReleaseCacheMu   sync.Mutex
	pluginReleaseCache     map[string]pluginReleaseCacheEntry
<<<<<<< HEAD
	imageTasksMu           sync.Mutex
	imageGenerationTasks   map[string]*imageGenerationTestTask
	startTime              time.Time
}

func (h *Handler) shareToken() string {
	if h == nil || h.cfg == nil {
		return ""
	}
	return strings.TrimSpace(h.cfg.RemoteManagement.ShareToken)
=======
>>>>>>> upstream/main
}

// NewHandler creates a new management handler instance.
func NewHandler(cfg *config.Config, configFilePath string, manager *coreauth.Manager) *Handler {
	envSecret, _ := os.LookupEnv("MANAGEMENT_PASSWORD")
	envSecret = strings.TrimSpace(envSecret)

	h := &Handler{
		cfg:                  cfg,
		configFilePath:       configFilePath,
		failedAttempts:       make(map[string]*attemptInfo),
		authManager:          manager,
		tokenStore:           sdkAuth.GetTokenStore(),
		allowRemoteOverride:  envSecret != "",
		envSecret:            envSecret,
		startTime:            time.Now(),
		imageGenerationTasks: make(map[string]*imageGenerationTestTask),
	}
	h.startAttemptCleanup()
	return h
}

// startAttemptCleanup launches a background goroutine that periodically
// removes stale IP entries from failedAttempts to prevent memory leaks.
func (h *Handler) startAttemptCleanup() {
	go func() {
		ticker := time.NewTicker(attemptCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.purgeStaleAttempts()
		}
	}()
}

// purgeStaleAttempts removes IP entries that have been idle beyond attemptMaxIdleTime
// and whose ban (if any) has expired.
func (h *Handler) purgeStaleAttempts() {
	now := time.Now()
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()
	for ip, ai := range h.failedAttempts {
		// Skip if still banned
		if !ai.blockedUntil.IsZero() && now.Before(ai.blockedUntil) {
			continue
		}
		// Remove if idle too long
		if now.Sub(ai.lastActivity) > attemptMaxIdleTime {
			delete(h.failedAttempts, ip)
		}
	}
}

// NewHandler creates a new management handler instance.
func NewHandlerWithoutConfigFilePath(cfg *config.Config, manager *coreauth.Manager) *Handler {
	return NewHandler(cfg, "", manager)
}

// SetConfig updates the in-memory config reference when the server hot-reloads.
func (h *Handler) SetConfig(cfg *config.Config) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.cfg = cfg
	h.mu.Unlock()
}

// SetAuthManager updates the auth manager reference used by management endpoints.
func (h *Handler) SetAuthManager(manager *coreauth.Manager) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.authManager = manager
	h.mu.Unlock()
}

// SetPluginHost updates the plugin host used by plugin-backed management endpoints.
func (h *Handler) SetPluginHost(host *pluginhost.Host) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pluginHost = host
	h.mu.Unlock()
}

// SetConfigReloadHook updates the callback used after management saves config changes.
func (h *Handler) SetConfigReloadHook(hook func(context.Context, *config.Config)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.configReloadHook = hook
	h.mu.Unlock()
}

func (h *Handler) reloadConfigAfterManagementSave(ctx context.Context, cfg *config.Config) {
	if h == nil || cfg == nil {
		return
	}
	h.mu.Lock()
	hook := h.configReloadHook
	host := h.pluginHost
	h.mu.Unlock()
	if hook != nil {
		hook(ctx, cfg)
		return
	}
	if host != nil {
		host.ApplyConfig(ctx, cfg)
	}
}

<<<<<<< HEAD
=======
func (h *Handler) reloadConfigAfterManagementSaveAsync(ctx context.Context, cfg *config.Config) {
	if h == nil || cfg == nil {
		return
	}
	reloadCtx := context.Background()
	if ctx != nil {
		reloadCtx = context.WithoutCancel(ctx)
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.WithField("panic", recovered).Error("management: async config reload panicked")
			}
		}()
		h.reloadConfigAfterManagementSave(reloadCtx, cfg)
	}()
}

>>>>>>> upstream/main
// SetLocalPassword configures the runtime-local password accepted for localhost requests.
func (h *Handler) SetLocalPassword(password string) { h.localPassword = password }

// SetLogDirectory updates the directory where main.log should be looked up.
func (h *Handler) SetLogDirectory(dir string) {
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	h.logDir = dir
}

// SetPostAuthHook registers a hook to be called after auth record creation but before persistence.
func (h *Handler) SetPostAuthHook(hook coreauth.PostAuthHook) {
	h.postAuthHook = hook
}

// SetPostAuthPersistHook registers a hook to be called after auth persistence.
func (h *Handler) SetPostAuthPersistHook(hook coreauth.PostAuthHook) {
	h.postAuthPersistHook = hook
}

// Middleware enforces access control for management endpoints.
// All requests (local and remote) require a valid management key.
// Additionally, remote access requires allow-remote-management=true.
func (h *Handler) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-CPA-VERSION", buildinfo.Version)
		c.Header("X-CPA-COMMIT", buildinfo.Commit)
		c.Header("X-CPA-BUILD-DATE", buildinfo.BuildDate)
		c.Header("X-CPA-SUPPORT-PLUGIN", pluginhost.SupportPluginHeaderValue())

		clientIP := c.ClientIP()
		localClient := clientIP == "127.0.0.1" || clientIP == "::1"

		// Accept either Authorization: Bearer <key> or X-Management-Key
		var provided string
		if ah := c.GetHeader("Authorization"); ah != "" {
			parts := strings.SplitN(ah, " ", 2)
			if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
				provided = parts[1]
			} else {
				provided = ah
			}
		}
		if provided == "" {
			provided = c.GetHeader("X-Management-Key")
		}
		if provided == "" {
			provided = strings.TrimSpace(c.Query("token"))
		}

		allowed, statusCode, errMsg := h.AuthenticateManagementKey(clientIP, localClient, provided)
		if !allowed {
			c.AbortWithStatusJSON(statusCode, gin.H{"error": errMsg})
			return
		}
		c.Next()
	}
}

// AuthenticateManagementKey verifies the provided management key for the given client.
// It mirrors the behaviour of Middleware() so non-HTTP callers can reuse the same logic.
func (h *Handler) AuthenticateManagementKey(clientIP string, localClient bool, provided string) (bool, int, string) {
	const maxFailures = 5
	const banDuration = 30 * time.Minute
	const failureDedupWindow = 1 * time.Second

	if h == nil {
		return false, http.StatusForbidden, "remote management disabled"
	}

	cfg := h.cfg
	var (
		allowRemote bool
		secretHash  string
	)
	if cfg != nil {
		allowRemote = cfg.RemoteManagement.AllowRemote
		secretHash = cfg.RemoteManagement.SecretKey
	}
	if h.allowRemoteOverride {
		allowRemote = true
	}
	envSecret := h.envSecret

	if !localClient && !allowRemote {
		return false, http.StatusForbidden, "remote management disabled"
	}

	if secretHash == "" && envSecret == "" {
		return false, http.StatusForbidden, "remote management key not set"
	}

	if provided == "" {
		return false, http.StatusUnauthorized, "missing management key"
	}

	keyValid := false
	if localClient {
		if lp := h.localPassword; lp != "" {
			if subtle.ConstantTimeCompare([]byte(provided), []byte(lp)) == 1 {
				keyValid = true
			}
		}
	}
	if !keyValid && envSecret != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(envSecret)) == 1 {
		keyValid = true
	}
	if !keyValid && envSecret != "" {
		envHash := sha256.Sum256([]byte(envSecret))
		envHashHex := hex.EncodeToString(envHash[:])
		if subtle.ConstantTimeCompare([]byte(provided), []byte(envHashHex)) == 1 {
			keyValid = true
		}
	}
	if !keyValid {
		if shareToken := h.shareToken(); shareToken != "" {
			if strings.HasPrefix(shareToken, "sha256:") {
				storedHash := strings.TrimPrefix(shareToken, "sha256:")
				providedHash := sha256.Sum256([]byte(provided))
				providedHashHex := hex.EncodeToString(providedHash[:])
				if subtle.ConstantTimeCompare([]byte(providedHashHex), []byte(storedHash)) == 1 {
					keyValid = true
				}
			} else {
				if subtle.ConstantTimeCompare([]byte(provided), []byte(shareToken)) == 1 {
					keyValid = true
				}
				if !keyValid {
					shareHash := sha256.Sum256([]byte(shareToken))
					shareHashHex := hex.EncodeToString(shareHash[:])
					if subtle.ConstantTimeCompare([]byte(provided), []byte(shareHashHex)) == 1 {
						keyValid = true
					}
				}
			}
		}
	}
	if !keyValid && secretHash != "" && bcrypt.CompareHashAndPassword([]byte(secretHash), []byte(provided)) == nil {
		keyValid = true
	}

	if keyValid {
		h.attemptsMu.Lock()
		delete(h.failedAttempts, clientIP)
		h.attemptsMu.Unlock()
		return true, 0, ""
	}

	now := time.Now()
	h.attemptsMu.Lock()
	ai := h.failedAttempts[clientIP]
	if ai != nil && !ai.blockedUntil.IsZero() && now.Before(ai.blockedUntil) {
		remaining := ai.blockedUntil.Sub(now).Round(time.Second)
		h.attemptsMu.Unlock()
		return false, http.StatusForbidden, fmt.Sprintf("IP banned due to too many failed attempts. Try again in %s", remaining)
	}
	if ai != nil && !ai.blockedUntil.IsZero() {
		ai.blockedUntil = time.Time{}
		ai.count = 0
	}
	if ai != nil && !ai.lastFailureAt.IsZero() && now.Sub(ai.lastFailureAt) < failureDedupWindow {
		h.attemptsMu.Unlock()
		return false, http.StatusUnauthorized, "invalid management key"
	}
	if ai == nil {
		ai = &attemptInfo{}
		h.failedAttempts[clientIP] = ai
	}
	ai.count++
	ai.lastFailureAt = now
	ai.lastActivity = now
	if ai.count >= maxFailures {
		ai.blockedUntil = now.Add(banDuration)
		ai.count = 0
	}
	h.attemptsMu.Unlock()

	return false, http.StatusUnauthorized, "invalid management key"
}

// persist saves the current in-memory config to disk.
func (h *Handler) persist(c *gin.Context) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.persistLocked(c)
}

// persistLocked saves the current in-memory config to disk.
// It expects the caller to hold h.mu.
func (h *Handler) persistLocked(c *gin.Context) bool {
	// Preserve comments when writing
	if err := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save config: %v", err)})
		return false
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
	return true
}

// Helper methods for simple types
func (h *Handler) updateBoolField(c *gin.Context, set func(bool)) {
	var body struct {
		Value *bool `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	set(*body.Value)
	h.persist(c)
}

func (h *Handler) updateIntField(c *gin.Context, set func(int)) {
	var body struct {
		Value *int `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	set(*body.Value)
	h.persist(c)
}

func (h *Handler) updateStringField(c *gin.Context, set func(string)) {
	var body struct {
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	set(*body.Value)
	h.persist(c)
}
