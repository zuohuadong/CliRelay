package helps

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestResolveAuthProxyURL(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global.example.com:7890"},
	}

	tests := []struct {
		name string
		cfg  *config.Config
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "auth proxy_url takes priority over global",
			cfg:  cfg,
			auth: &cliproxyauth.Auth{ProxyURL: "socks5://auth.example.com:1080"},
			want: "socks5://auth.example.com:1080",
		},
		{
			name: "auth metadata proxy_url takes priority over global",
			cfg:  cfg,
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"proxy_url": "http://meta.example.com:8080"}},
			want: "http://meta.example.com:8080",
		},
		{
			name: "auth proxy_id with scheme takes priority over global",
			cfg:  cfg,
			auth: &cliproxyauth.Auth{ProxyID: "socks5://id.example.com:1080"},
			want: "socks5://id.example.com:1080",
		},
		{
			name: "no credential proxy falls back to global default",
			cfg:  cfg,
			auth: &cliproxyauth.Auth{},
			want: "http://global.example.com:7890",
		},
		{
			name: "nil auth falls back to global default",
			cfg:  cfg,
			auth: nil,
			want: "http://global.example.com:7890",
		},
		{
			name: "nil config returns auth proxy",
			cfg:  nil,
			auth: &cliproxyauth.Auth{ProxyURL: "socks5://auth.example.com:1080"},
			want: "socks5://auth.example.com:1080",
		},
		{
			name: "nil config and nil auth returns empty",
			cfg:  nil,
			auth: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveAuthProxyURL(tt.cfg, tt.auth); got != tt.want {
				t.Fatalf("ResolveAuthProxyURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
