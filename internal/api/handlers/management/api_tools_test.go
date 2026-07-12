package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// The APICall HTTP handler is the stable public seam for credential-specific
// management tests. A disabled credential must be rejected before this handler
// resolves a token or creates an upstream request.
func TestAPICallRejectsDisabledAuthWithoutUpstreamRequest(t *testing.T) {
	tests := []struct {
		name string
		auth *coreauth.Auth
	}{
		{
			name: "disabled flag",
			auth: &coreauth.Auth{
				ID:       "api-call-disabled-flag",
				Provider: "astron-code",
				Disabled: true,
				Attributes: map[string]string{
					"api_key": "disabled-flag-token",
				},
			},
		},
		{
			name: "disabled status",
			auth: &coreauth.Auth{
				ID:       "api-call-disabled-status",
				Provider: "astron-code",
				Status:   coreauth.StatusDisabled,
				Attributes: map[string]string{
					"api_key": "disabled-status-token",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			manager := coreauth.NewManager(nil, nil, nil)
			if _, errRegister := manager.Register(t.Context(), tt.auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			h := &Handler{authManager: manager}

			body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, tt.auth.EnsureIndex(), upstream.URL)
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
			ctx.Request.Header.Set("Content-Type", "application/json")

			h.APICall(ctx)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "auth is disabled") {
				t.Fatalf("response body = %s, want disabled auth error", rec.Body.String())
			}
			if got := upstreamCalls.Load(); got != 0 {
				t.Fatalf("upstream requests = %d, want 0", got)
			}
		})
	}
}

func TestAPICallEnabledAuthReplacesTokenAndCallsUpstream(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer enabled-token" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer enabled-token")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "api-call-enabled",
		Provider: "astron-code",
		Attributes: map[string]string{
			"api_key": "enabled-token",
		},
	}
	if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	h := &Handler{authManager: manager}

	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, auth.EnsureIndex(), upstream.URL)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
	var response apiCallResponse
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upstream status = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if response.Body != "ok" {
		t.Fatalf("upstream body = %q, want %q", response.Body, "ok")
	}
}

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
			AstronCodeAPIKey: []config.OpenAICompatibility{{
				Name:    "astron-code",
				BaseURL: "https://maas-coding-api.cn-huabei-1.xf-yun.com/v2",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "astron-key",
					ProxyURL: "http://astron-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
		{
			name: "astron-code",
			auth: &coreauth.Auth{
				Provider: "astron-code",
				Attributes: map[string]string{
					"api_key":      "astron-key",
					"compat_name":  "astron-code",
					"provider_key": "astron-code",
				},
			},
			wantProxy: "http://astron-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth)
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}
