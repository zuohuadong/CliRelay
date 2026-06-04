package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPutIdentityFingerprintAcceptsWrappedPayload(t *testing.T) {
	const ua = "codex-tui/0.135.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.135.0)"
	cfg := &config.Config{}
	h := newIdentityFingerprintTestHandler(t, cfg)

	body := `{
		"identity-fingerprint": {
			"codex": {
				"enabled": true,
				"user-agent": "` + ua + `",
				"version": "0.135.0",
				"originator": "codex-tui",
				"websocket-beta": "responses_websockets=2026-02-06",
				"session-mode": "per-request"
			},
			"claude": {
				"enabled": false
			}
		},
		"defaults": {
			"codex": {}
		}
	}`

	rec := runIdentityFingerprintRequest(t, h, http.MethodPut, body, h.PutIdentityFingerprint)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cfg.IdentityFingerprint.Codex.UserAgent; got != ua {
		t.Fatalf("expected user-agent %q, got %q", ua, got)
	}
	if !cfg.IdentityFingerprint.Codex.Enabled {
		t.Fatal("expected codex fingerprint to be enabled")
	}
	if got := cfg.IdentityFingerprint.Codex.Version; got != "0.135.0" {
		t.Fatalf("expected version to be preserved, got %q", got)
	}
}

func TestPatchIdentityFingerprintPreservesUnspecifiedProvider(t *testing.T) {
	cfg := &config.Config{
		IdentityFingerprint: config.IdentityFingerprintConfig{
			Claude: config.ClaudeIdentityFingerprintConfig{
				Enabled:    true,
				UserAgent:  "claude-cli/2.0.0 (external, terminal)",
				CLIVersion: "2.0.0",
				Entrypoint: "terminal",
			},
		},
	}
	h := newIdentityFingerprintTestHandler(t, cfg)

	body := `{
		"codex": {
			"enabled": true,
			"user-agent": "codex-tui/0.135.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.135.0)",
			"version": "0.135.0",
			"originator": "codex-tui",
			"session-mode": "per-request"
		}
	}`

	rec := runIdentityFingerprintRequest(t, h, http.MethodPatch, body, h.PatchIdentityFingerprint)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !cfg.IdentityFingerprint.Claude.Enabled {
		t.Fatal("expected patch to preserve existing claude fingerprint")
	}
	if got := cfg.IdentityFingerprint.Claude.UserAgent; got != "claude-cli/2.0.0 (external, terminal)" {
		t.Fatalf("expected existing claude user-agent to be preserved, got %q", got)
	}
}

func TestPatchOpenAICompatPreservesIdentityFingerprint(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "qwen",
				BaseURL: "https://example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-test"},
				},
			},
		},
	}
	h := newIdentityFingerprintTestHandler(t, cfg)

	body := `{
		"name": "qwen",
		"value": {
			"identity-fingerprint": " CODEX "
		}
	}`

	rec := runIdentityFingerprintRequest(t, h, http.MethodPatch, body, h.PatchOpenAICompat)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cfg.OpenAICompatibility[0].IdentityFingerprint; got != "codex" {
		t.Fatalf("expected normalized identity fingerprint %q, got %q", "codex", got)
	}
}

func newIdentityFingerprintTestHandler(t *testing.T, cfg *config.Config) *Handler {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return &Handler{
		cfg:            cfg,
		configFilePath: configPath,
		failedAttempts: make(map[string]*attemptInfo),
	}
}

func runIdentityFingerprintRequest(t *testing.T, h *Handler, method, body string, fn func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, "/v0/management/identity-fingerprint", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	fn(ctx)
	return rec
}
