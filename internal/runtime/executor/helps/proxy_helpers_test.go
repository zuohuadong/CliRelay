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

func TestNewDevinHTTPClient_ReusesTransportFromContext(t *testing.T) {
	baseTransport := &http.Transport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", baseTransport)

	c1 := NewDevinHTTPClient(ctx, nil, nil, 0)
	c2 := NewDevinHTTPClient(ctx, nil, nil, 0)

	if c1.Transport != c2.Transport {
		t.Errorf("expected c1.Transport == c2.Transport across requests, got different pointers %p vs %p", c1.Transport, c2.Transport)
	}

	tr, ok := c1.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c1.Transport)
	}
	if !tr.DisableCompression {
		t.Error("expected DisableCompression = true")
	}
}

func TestNewDevinHTTPClient_NonStandardRoundTripperDisablesGzip(t *testing.T) {
	var seenEncoding string
	customRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenEncoding = req.Header.Get("Accept-Encoding")
		return &http.Response{StatusCode: 200}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customRT)

	c := NewDevinHTTPClient(ctx, nil, nil, 0)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid", nil)
	_, _ = c.Transport.RoundTrip(req)

	if seenEncoding != "identity" {
		t.Errorf("expected Accept-Encoding: identity, got %q", seenEncoding)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
