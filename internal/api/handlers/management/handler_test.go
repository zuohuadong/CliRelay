package management

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
)

func TestAuthenticateManagementKeyCachesSuccessfulBcryptVerification(t *testing.T) {
	now := time.Unix(1_000, 0)
	verifyCalls := 0
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				AllowRemote: true,
				SecretKey:   "hash-a",
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		managementPasswordVerifier: func(hash, password []byte) error {
			verifyCalls++
			if string(hash) == "hash-a" && string(password) == "secret-a" {
				return nil
			}
			return errors.New("password mismatch")
		},
		managementAuthCacheTTL: time.Minute,
		managementAuthCacheNow: func() time.Time {
			return now
		},
	}

	for i := 0; i < 4; i++ {
		allowed, statusCode, errMsg := h.AuthenticateManagementKey("203.0.113.10", false, "secret-a")
		if !allowed {
			t.Fatalf("request %d rejected: status=%d message=%q", i+1, statusCode, errMsg)
		}
	}
	if verifyCalls != 1 {
		t.Fatalf("bcrypt verifier calls = %d, want 1 for repeated valid credential", verifyCalls)
	}

	allowed, statusCode, _ := h.AuthenticateManagementKey("203.0.113.10", false, "wrong-secret")
	if allowed || statusCode != http.StatusUnauthorized {
		t.Fatalf("wrong credential result = allowed:%t status:%d", allowed, statusCode)
	}
	if verifyCalls != 2 {
		t.Fatalf("failed credential must not reuse success cache, verifier calls = %d", verifyCalls)
	}

	now = now.Add(2 * time.Minute)
	allowed, statusCode, errMsg := h.AuthenticateManagementKey("203.0.113.10", false, "secret-a")
	if !allowed {
		t.Fatalf("valid credential after expiry rejected: status=%d message=%q", statusCode, errMsg)
	}
	if verifyCalls != 3 {
		t.Fatalf("expired cache verifier calls = %d, want 3", verifyCalls)
	}
}

func TestAuthenticateManagementKeyCoalescesConcurrentBcryptVerification(t *testing.T) {
	var verifyCalls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				AllowRemote: true,
				SecretKey:   "hash-a",
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		managementPasswordVerifier: func(hash, password []byte) error {
			if verifyCalls.Add(1) == 1 {
				close(started)
			}
			<-release
			if string(hash) == "hash-a" && string(password) == "secret-a" {
				return nil
			}
			return errors.New("password mismatch")
		},
	}

	const callers = 8
	results := make(chan bool, callers)
	for i := 0; i < callers; i++ {
		go func() {
			allowed, _, _ := h.AuthenticateManagementKey("203.0.113.10", false, "secret-a")
			results <- allowed
		}()
	}
	<-started
	time.Sleep(25 * time.Millisecond)
	close(release)
	for i := 0; i < callers; i++ {
		if !<-results {
			t.Fatalf("caller %d was rejected", i+1)
		}
	}
	if got := verifyCalls.Load(); got != 1 {
		t.Fatalf("concurrent bcrypt verifier calls = %d, want 1", got)
	}
}

func TestAuthenticateManagementKeyRejectsInFlightVerificationAfterSecretRotation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				AllowRemote: true,
				SecretKey:   "hash-a",
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		managementPasswordVerifier: func(hash, password []byte) error {
			if string(hash) == "hash-a" {
				close(started)
				<-release
			}
			if string(hash) == "hash-a" && string(password) == "secret-a" {
				return nil
			}
			return errors.New("password mismatch")
		},
	}

	result := make(chan bool, 1)
	go func() {
		allowed, _, _ := h.AuthenticateManagementKey("203.0.113.10", false, "secret-a")
		result <- allowed
	}()
	<-started
	h.SetConfig(&config.Config{
		RemoteManagement: config.RemoteManagement{
			AllowRemote: true,
			SecretKey:   "hash-b",
		},
	})
	close(release)
	if <-result {
		t.Fatal("old management secret succeeded after SetConfig rotated the key")
	}
}

func TestManagementAuthCacheUsesProcessSecretAndPurgesExpiredEntries(t *testing.T) {
	now := time.Unix(3_000, 0)
	left := &Handler{
		managementAuthCacheTTL: time.Minute,
		managementAuthCacheNow: func() time.Time {
			return now
		},
	}
	right := &Handler{}
	leftKey, leftOK := left.managementAuthCacheDigest(7, "hash-a", "human-password")
	rightKey, rightOK := right.managementAuthCacheDigest(7, "hash-a", "human-password")
	if !leftOK || !rightOK {
		t.Fatal("failed to initialize process-secret cache digest")
	}
	if leftKey == rightKey {
		t.Fatal("independent handlers produced the same management auth cache digest")
	}
	fastDigest := sha256.Sum256([]byte("hash-a\x00human-password"))
	if leftKey == fastDigest {
		t.Fatal("management auth cache digest must not be an unkeyed SHA-256 password verifier")
	}

	for i := 0; i < managementAuthCacheMaxEntries+8; i++ {
		key, ok := left.managementAuthCacheDigest(7, "hash-a", fmt.Sprintf("secret-%d", i))
		if !ok {
			t.Fatal("failed to derive bounded cache key")
		}
		left.storeManagementAuthCache(key)
	}
	left.managementAuthCacheMu.Lock()
	cacheLen := len(left.managementAuthCache)
	left.managementAuthCacheMu.Unlock()
	if cacheLen > managementAuthCacheMaxEntries {
		t.Fatalf("management auth cache entries = %d, want at most %d", cacheLen, managementAuthCacheMaxEntries)
	}

	now = now.Add(2 * time.Minute)
	left.purgeExpiredManagementAuthCache()
	left.managementAuthCacheMu.Lock()
	cacheLen = len(left.managementAuthCache)
	left.managementAuthCacheMu.Unlock()
	if cacheLen != 0 {
		t.Fatalf("expired management auth cache entries retained = %d", cacheLen)
	}
}

func TestAuthenticateManagementKey_CorrectKeyBypassesBan(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	for i := 0; i < 5; i++ {
		allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed {
			t.Fatalf("expected auth to be denied at attempt %d", i+1)
		}
		if statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("unexpected auth failure at attempt %d: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}

	allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if !allowed {
		t.Fatalf("expected correct key to bypass ban: status=%d msg=%q", statusCode, errMsg)
	}
}

func TestAuthenticateManagementKey_WrongKeyAfterBanIsBlocked(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{AllowRemote: true},
		},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	for i := 0; i < 5; i++ {
		h.AuthenticateManagementKey("1.2.3.4", false, "wrong-secret")
		time.Sleep(1100 * time.Millisecond)
	}

	allowed, statusCode, errMsg := h.AuthenticateManagementKey("1.2.3.4", false, "still-wrong")
	if allowed {
		t.Fatalf("expected wrong key to be blocked after ban")
	}
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected forbidden status while banned, got %d: %q", statusCode, errMsg)
	}
	if !strings.HasPrefix(errMsg, "IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected banned message: %q", errMsg)
	}

	allowed, _, _ = h.AuthenticateManagementKey("1.2.3.4", false, "test-secret")
	if !allowed {
		t.Fatalf("expected correct key to bypass ban")
	}
}

func TestAuthenticateManagementKey_ConcurrentFailuresDeduplicated(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{AllowRemote: true},
		},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	for i := 0; i < 10; i++ {
		h.AuthenticateManagementKey("1.2.3.4", false, "wrong-secret")
	}

	ai := h.failedAttempts["1.2.3.4"]
	if ai == nil {
		t.Fatal("expected attempt info for IP")
	}
	if ai.count >= 5 {
		t.Fatalf("expected deduplication to prevent ban trigger, got count=%d", ai.count)
	}
	if !ai.blockedUntil.IsZero() {
		t.Fatalf("expected no ban due to deduplication, but ban is set until %v", ai.blockedUntil)
	}
}

func TestMiddlewareSetsSupportPluginHeader(t *testing.T) {

	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	middleware := h.Middleware()

	t.Run("invalid key", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
		c.Request.RemoteAddr = "127.0.0.1:12345"
		c.Request.Header.Set("X-Management-Key", "wrong-secret")

		middleware(c)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Header().Get("X-CPA-SUPPORT-PLUGIN"); got != pluginhost.SupportPluginHeaderValue() {
			t.Fatalf("X-CPA-SUPPORT-PLUGIN = %q, want %q", got, pluginhost.SupportPluginHeaderValue())
		}
	})

	t.Run("valid key", func(t *testing.T) {
		engine := gin.New()
		engine.GET("/v0/management/config", middleware, func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Management-Key", "test-secret")
		engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("X-CPA-SUPPORT-PLUGIN"); got != pluginhost.SupportPluginHeaderValue() {
			t.Fatalf("X-CPA-SUPPORT-PLUGIN = %q, want %q", got, pluginhost.SupportPluginHeaderValue())
		}
	})
}
