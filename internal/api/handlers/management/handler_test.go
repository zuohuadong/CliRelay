package management

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

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
