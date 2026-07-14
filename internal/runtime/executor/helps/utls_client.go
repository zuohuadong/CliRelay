package helps

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper implements http.RoundTripper using utls with Chrome fingerprint
// to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string][]http2ClientConn
	pending     map[string]chan struct{}
	dialer      proxy.Dialer
}

type http2ClientConn interface {
	RoundTrip(*http.Request) (*http.Response, error)
	CanTakeNewRequest() bool
	ReserveNewRequest() bool
	State() http2.ClientConnState
	Close() error
}

const utlsHTTP2IdleConnTimeout = 90 * time.Second

func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	var dialer proxy.Dialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return newUtlsRoundTripperWithDialer(dialer)
}

func newUtlsRoundTripperWithDialer(dialer proxy.Dialer) *utlsRoundTripper {
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	}
	return &utlsRoundTripper{
		connections: make(map[string][]http2ClientConn),
		pending:     make(map[string]chan struct{}),
		dialer:      dialer,
	}
}

// NewStrictUtlsHTTPClient builds a uTLS-capable client that must use proxyURL.
// Any proxy parse/build failure is returned to the caller instead of falling back.
func NewStrictUtlsHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	dialer, mode, err := proxyutil.BuildDialer(proxyURL)
	if err != nil {
		return nil, err
	}
	if mode != proxyutil.ModeProxy || dialer == nil {
		return nil, fmt.Errorf("strict proxy URL must select proxy mode")
	}
	standardTransport, standardMode, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	if standardMode != proxyutil.ModeProxy || standardTransport == nil {
		return nil, fmt.Errorf("strict proxy URL must build an HTTP transport")
	}
	client := &http.Client{
		Transport: &fallbackRoundTripper{
			utls:     newUtlsRoundTripperWithDialer(dialer),
			fallback: standardTransport,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client, nil
}

func (t *utlsRoundTripper) getOrCreateConnection(ctx context.Context, host, addr string) (http2ClientConn, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		t.mu.Lock()
		connections := t.connections[host]
		kept := connections[:0]
		var selected http2ClientConn
		var retired []http2ClientConn
		for _, h2Conn := range connections {
			state := h2Conn.State()
			if state.Closed || state.Closing {
				retired = append(retired, h2Conn)
				continue
			}
			if selected == nil && h2Conn.ReserveNewRequest() {
				selected = h2Conn
			}
			kept = append(kept, h2Conn)
		}
		if len(kept) == 0 {
			delete(t.connections, host)
		} else {
			t.connections[host] = kept
		}
		if selected != nil {
			t.mu.Unlock()
			for _, h2Conn := range retired {
				retireHTTP2Connection(h2Conn)
			}
			return selected, nil
		}
		if ready, ok := t.pending[host]; ok {
			t.mu.Unlock()
			for _, h2Conn := range retired {
				retireHTTP2Connection(h2Conn)
			}
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		ready := make(chan struct{})
		t.pending[host] = ready
		t.mu.Unlock()
		for _, h2Conn := range retired {
			retireHTTP2Connection(h2Conn)
		}

		h2Conn, err := t.createConnection(ctx, host, addr)

		t.mu.Lock()
		delete(t.pending, host)
		close(ready)
		if err != nil {
			t.mu.Unlock()
			return nil, err
		}
		if errCtx := ctx.Err(); errCtx != nil {
			t.mu.Unlock()
			_ = h2Conn.Close()
			return nil, errCtx
		}
		if !h2Conn.ReserveNewRequest() {
			t.mu.Unlock()
			retireHTTP2Connection(h2Conn)
			continue
		}
		t.connections[host] = append(t.connections[host], h2Conn)
		t.mu.Unlock()
		return h2Conn, nil
	}
}

func (t *utlsRoundTripper) createConnection(ctx context.Context, host, addr string) (http2ClientConn, error) {
	conn, err := dialProxyContext(ctx, t.dialer, "tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}

	tr := newUtlsHTTP2Transport()
	h2Conn, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}

	return h2Conn, nil
}

func newUtlsHTTP2Transport() *http2.Transport {
	return &http2.Transport{IdleConnTimeout: utlsHTTP2IdleConnTimeout}
}

func retireHTTP2Connection(h2Conn http2ClientConn) {
	state := h2Conn.State()
	if state.StreamsActive > 0 || state.StreamsReserved > 0 || state.StreamsPending > 0 {
		// 已有流或已预留的请求必须继续使用该连接；GOAWAY/do-not-reuse
		// 会在最后一个流结束后由 http2.ClientConn 自行关闭底层连接。
		return
	}
	_ = h2Conn.Close()
}

func (t *utlsRoundTripper) removeConnection(host string, target http2ClientConn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	connections := t.connections[host]
	kept := connections[:0]
	for _, h2Conn := range connections {
		if h2Conn != target {
			kept = append(kept, h2Conn)
		}
	}
	if len(kept) == 0 {
		delete(t.connections, host)
		return
	}
	t.connections[host] = kept
}

func dialProxyContext(ctx context.Context, dialer proxy.Dialer, network, addr string) (net.Conn, error) {
	if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
		return contextDialer.DialContext(ctx, network, addr)
	}
	return nil, fmt.Errorf("uTLS dialer %T does not support context cancellation", dialer)
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	h2Conn, err := t.getOrCreateConnection(req.Context(), hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		state := h2Conn.State()
		if !state.Closed && !state.Closing {
			return nil, err
		}
		t.removeConnection(hostname, h2Conn)
		retireHTTP2Connection(h2Conn)
		return nil, err
	}

	return resp, nil
}

// utlsProtectedHosts contains the hosts that should use utls Chrome TLS fingerprint
// to bypass Cloudflare's TLS fingerprinting.
var utlsProtectedHosts = map[string]struct{}{
	"api.anthropic.com": {},
	"chatgpt.com":       {},
}

// fallbackRoundTripper uses utls for protected HTTPS hosts and falls back to
// standard transport for all other requests.
type fallbackRoundTripper struct {
	utls     http.RoundTripper
	fallback http.RoundTripper
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		if _, ok := utlsProtectedHosts[strings.ToLower(req.URL.Hostname())]; ok {
			return f.utls.RoundTrip(req)
		}
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client using utls Chrome TLS fingerprint.
// Use this for provider requests that need a Chrome-like TLS fingerprint.
// Falls back to standard transport for non-HTTPS requests.
func NewUtlsHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	var ctxRoundTripper http.RoundTripper
	if ctx != nil {
		ctxRoundTripper, _ = ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	}

	var utlsRT http.RoundTripper = newUtlsRoundTripper(proxyURL)
	var standardTransport http.RoundTripper = http.DefaultTransport
	if proxyURL != "" {
		if transport := buildProxyTransport(proxyURL); transport != nil {
			standardTransport = transport
		}
	} else if ctxRoundTripper != nil {
		utlsRT = ctxRoundTripper
		standardTransport = ctxRoundTripper
	}

	client := &http.Client{
		Transport: &fallbackRoundTripper{
			utls:     utlsRT,
			fallback: standardTransport,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
