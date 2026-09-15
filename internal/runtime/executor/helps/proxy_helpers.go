package helps

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// ResolveAuthProxyURL returns the proxy URL a credential should use for outbound traffic
// and background token refreshes. It checks auth.ProxyURL, auth.Metadata["proxy_url"],
// auth.ProxyID / auth.Metadata["proxy_id"] (when containing a URL/scheme), and falls back to cfg.ProxyURL.
func ResolveAuthProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
		if auth.Metadata != nil {
			if proxyURL, ok := auth.Metadata["proxy_url"].(string); ok && strings.TrimSpace(proxyURL) != "" {
				return strings.TrimSpace(proxyURL)
			}
		}
		if proxyID := strings.TrimSpace(auth.ProxyID); proxyID != "" {
			if strings.Contains(proxyID, "://") {
				return proxyID
			}
		}
		if auth.Metadata != nil {
			if proxyID, ok := auth.Metadata["proxy_id"].(string); ok && strings.TrimSpace(proxyID) != "" {
				if strings.Contains(proxyID, "://") {
					return strings.TrimSpace(proxyID)
				}
			}
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use credential-scoped proxy if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// Priority 1 & 2: Use credential-scoped proxy or fallback to cfg.ProxyURL
	proxyURL := ResolveAuthProxyURL(cfg, auth)

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	}

	return httpClient
}

// NewStrictProxyHTTPClient builds a client that must use the provided proxy.
// It never consults environment proxies, context transports, or the host's direct dialer.
func NewStrictProxyHTTPClient(proxyURL string, timeout, responseHeaderTimeout time.Duration) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	transport, mode, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("build strict proxy transport: %w", err)
	}
	if mode != proxyutil.ModeProxy || transport == nil {
		return nil, fmt.Errorf("strict proxy URL must select proxy mode")
	}
	if responseHeaderTimeout > 0 {
		transport.ResponseHeaderTimeout = responseHeaderTimeout
	}
	client := &http.Client{Transport: transport}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client, nil
}

var devinTransportCache = NewTransportCache[string](DefaultTransportCacheCapacity)

// NewDevinHTTPClient creates an HTTP client customized for Devin Connect-RPC upstream.
// Suppresses automatic Accept-Encoding: gzip while preserving connection reuse across requests.
func NewDevinHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	// Respect explicitly injected context RoundTripper (e.g. from Conductor, Home, or integration test fixtures)
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			if tr, ok := rt.(*http.Transport); ok {
				key := fmt.Sprintf("rt:%p", tr)
				cloned, err := devinTransportCache.Get(key, func() (*http.Transport, error) {
					c := tr.Clone()
					c.DisableCompression = true
					return c, nil
				})
				if err == nil && cloned != nil {
					return &http.Client{
						Transport: cloned,
						Timeout:   timeout,
					}
				}
			}
			return &http.Client{
				Transport: devinNoGzipRoundTripper{base: rt},
				Timeout:   timeout,
			}
		}
	}

	proxyURL := ""
	if auth != nil && strings.TrimSpace(auth.ProxyURL) != "" {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	} else if cfg != nil && strings.TrimSpace(cfg.ProxyURL) != "" {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	tr, err := devinTransportCache.Get(proxyURL, func() (*http.Transport, error) {
		var base *http.Transport
		if proxyURL != "" {
			base = buildProxyTransport(proxyURL)
		}
		if base == nil {
			if dt, ok := http.DefaultTransport.(*http.Transport); ok {
				base = dt.Clone()
			} else {
				base = &http.Transport{}
			}
		}
		base.DisableCompression = true
		return base, nil
	})
	if err != nil || tr == nil {
		tr = &http.Transport{DisableCompression: true}
	}

	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
}

type devinNoGzipRoundTripper struct {
	base http.RoundTripper
}

func (rt devinNoGzipRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "identity")
	}
	return rt.base.RoundTrip(req)
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}

// NewProxyAwareHTTPClientWithResponseHeaderTimeout creates an HTTP client and sets
// ResponseHeaderTimeout on the transport when a positive duration is provided.
// This is used by the compact endpoint to avoid waiting too long for slow upstreams.
func NewProxyAwareHTTPClientWithResponseHeaderTimeout(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout, responseHeaderTimeout time.Duration) *http.Client {
	httpClient := NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	if responseHeaderTimeout <= 0 || httpClient == nil {
		return httpClient
	}
	if transport, ok := httpClient.Transport.(*http.Transport); ok && transport != nil {
		transport.ResponseHeaderTimeout = responseHeaderTimeout
	}
	return httpClient
}
