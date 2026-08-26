package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestOAuthProxyURLFromRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example:8080"},
	}

	tests := []struct {
		name         string
		url          string
		cfg          *config.Config
		wantProxyURL string
		wantProxyID  string
		wantPersist  bool
		wantErr      bool
	}{
		{
			name:         "explicit proxy_url parameter",
			url:          "/auth?proxy_url=socks5://explicit.example:1080",
			cfg:          cfg,
			wantProxyURL: "socks5://explicit.example:1080",
			wantProxyID:  "",
			wantPersist:  true,
		},
		{
			name:         "explicit proxy-url parameter",
			url:          "/auth?proxy-url=http://dash.example:8080",
			cfg:          cfg,
			wantProxyURL: "http://dash.example:8080",
			wantProxyID:  "",
			wantPersist:  true,
		},
		{
			name:         "proxy_id parameter with URL scheme",
			url:          "/auth?proxy_id=socks5://id.example:1080",
			cfg:          cfg,
			wantProxyURL: "socks5://id.example:1080",
			wantProxyID:  "socks5://id.example:1080",
			wantPersist:  true,
		},
		{
			name:         "invalid explicit proxy_url",
			url:          "/auth?proxy_url=bad-value",
			cfg:          cfg,
			wantProxyURL: "",
			wantProxyID:  "",
			wantPersist:  false,
			wantErr:      true,
		},
		{
			name:         "proxy_id parameter identifier only",
			url:          "/auth?proxy_id=us-node-1",
			cfg:          cfg,
			wantProxyURL: "http://global-proxy.example:8080",
			wantProxyID:  "us-node-1",
			wantPersist:  false,
			wantErr:      true,
		},
		{
			name:         "no proxy params falls back to global config",
			url:          "/auth",
			cfg:          cfg,
			wantProxyURL: "http://global-proxy.example:8080",
			wantProxyID:  "",
			wantPersist:  false,
		},
		{
			name:         "no proxy params with nil config",
			url:          "/auth",
			cfg:          nil,
			wantProxyURL: "",
			wantProxyID:  "",
			wantPersist:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, tt.url, nil)

			gotURL, gotID, gotPersist, err := oauthProxyURLFromRequest(c, tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected proxy_id validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("oauthProxyURLFromRequest() error = %v", err)
			}
			if gotURL != tt.wantProxyURL {
				t.Fatalf("proxyURL = %q, want %q", gotURL, tt.wantProxyURL)
			}
			if gotID != tt.wantProxyID {
				t.Fatalf("proxyID = %q, want %q", gotID, tt.wantProxyID)
			}
			if gotPersist != tt.wantPersist {
				t.Fatalf("persist = %v, want %v", gotPersist, tt.wantPersist)
			}
		})
	}
}

func TestApplyOAuthProxyDoesNotPersistGlobalFallback(t *testing.T) {
	metadata := map[string]any{}
	record := &coreauth.Auth{Metadata: metadata}

	applyOAuthProxy(record, metadata, "http://global-proxy.example:8080", "", false)

	if record.ProxyURL != "" || record.ProxyID != "" {
		t.Fatalf("global fallback must not be persisted: %#v", record)
	}
	if _, ok := metadata["proxy_url"]; ok {
		t.Fatal("global fallback must not be copied to metadata")
	}
}
