package executor

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type staticEgressResolver struct {
	resolved egress.ResolvedEndpoint
	err      error
}

func (r staticEgressResolver) Resolve(context.Context, string) (egress.ResolvedEndpoint, error) {
	return r.resolved, r.err
}

func TestCodexStrictEgressBlocksAllHTTPPathsBeforeDirectDial(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	var homeRefreshCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	exec := NewCodexExecutorWithEgress(&config.Config{}, staticEgressResolver{err: egress.ErrEgressRequired})
	exec.homeRefresh = func(context.Context, *config.Config, *cliproxyauth.Auth) (*cliproxyauth.Auth, bool, error) {
		homeRefreshCalls.Add(1)
		return &cliproxyauth.Auth{Provider: "codex"}, true, nil
	}
	auth := &cliproxyauth.Auth{
		ID:       "codex.json",
		Metadata: map[string]any{"account_id": "acct-123", "refresh_token": "refresh"},
		Attributes: map[string]string{
			"base_url": upstream.URL,
			"api_key":  "test",
		},
	}

	req, _ := http.NewRequest(http.MethodGet, upstream.URL, nil)
	if _, err := exec.HttpRequest(context.Background(), auth, req); !errors.Is(err, egress.ErrEgressRequired) {
		t.Fatalf("HttpRequest() error = %v", err)
	}
	if _, err := exec.Refresh(context.Background(), auth); !errors.Is(err, egress.ErrEgressRequired) {
		t.Fatalf("Refresh() error = %v", err)
	}
	if _, err := exec.ProbeQuotaRecovery(context.Background(), auth); !errors.Is(err, egress.ErrEgressRequired) {
		t.Fatalf("ProbeQuotaRecovery() error = %v", err)
	}

	cases := []struct {
		name string
		req  cliproxyexecutor.Request
		opts cliproxyexecutor.Options
	}{
		{
			name: "responses http",
			req:  cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`)},
			opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")},
		},
		{
			name: "compact",
			req:  cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`)},
			opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Alt: "responses/compact"},
		},
		{
			name: "image",
			req:  cliproxyexecutor.Request{Model: "gpt-image-2", Payload: []byte(`{"model":"gpt-image-2","prompt":"cat"}`)},
			opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(codexOpenAIImageSourceFormat), Metadata: map[string]any{cliproxyexecutor.RequestPathMetadataKey: codexImagesGenerationsPath}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exec.Execute(context.Background(), auth, tc.req, tc.opts); !errors.Is(err, egress.ErrEgressRequired) {
				t.Fatalf("Execute() error = %v", err)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("upstream direct hits = %d, want 0", hits.Load())
	}
	if homeRefreshCalls.Load() != 0 {
		t.Fatalf("home refresh calls = %d, want 0", homeRefreshCalls.Load())
	}
}

func TestCodexStrictEgressRefreshSkipsHomeAndUsesResolvedProxy(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if r.Method != http.MethodConnect {
			t.Errorf("proxy method = %s, want CONNECT", r.Method)
		}
		cancel()
		w.WriteHeader(http.StatusForbidden)
	}))
	defer proxy.Close()

	var homeRefreshCalls atomic.Int32
	exec := NewCodexExecutorWithEgress(
		&config.Config{Home: config.HomeConfig{Enabled: true}},
		staticEgressResolver{resolved: egress.ResolvedEndpoint{ProxyURL: proxy.URL}},
	)
	exec.homeRefresh = func(context.Context, *config.Config, *cliproxyauth.Auth) (*cliproxyauth.Auth, bool, error) {
		homeRefreshCalls.Add(1)
		return &cliproxyauth.Auth{Provider: "codex"}, true, nil
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{
			"account_id":    "acct-123",
			"refresh_token": "refresh-token",
		},
	}

	if _, err := exec.Refresh(ctx, auth); err == nil {
		t.Fatal("Refresh() error = nil, want strict proxy rejection")
	}
	if got := homeRefreshCalls.Load(); got != 0 {
		t.Fatalf("home refresh calls = %d, want 0", got)
	}
	if got := proxyHits.Load(); got != 1 {
		t.Fatalf("strict proxy hits = %d, want 1", got)
	}
}

func TestCodexStrictEgressRoutesHttpRequestThroughResolvedProxy(t *testing.T) {
	t.Parallel()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.String(); got != "http://upstream.invalid/test" {
			t.Fatalf("proxy URL = %q", got)
		}
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxy.Close()

	exec := NewCodexExecutorWithEgress(&config.Config{}, staticEgressResolver{resolved: egress.ResolvedEndpoint{ProxyURL: proxy.URL}})
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"account_id": "acct-123"}}
	req, _ := http.NewRequest(http.MethodGet, "http://upstream.invalid/test", nil)
	resp, err := exec.HttpRequest(context.Background(), auth, req)
	if err != nil {
		t.Fatalf("HttpRequest() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if strings.TrimSpace(resp.Header.Get("Via")) != "" {
		t.Fatalf("unexpected Via header = %q", resp.Header.Get("Via"))
	}
}

func TestCodexStrictEgressInvalidProxyNeverFallsBackToDirect(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("direct"))
	}))
	defer upstream.Close()

	exec := NewCodexExecutorWithEgress(&config.Config{}, staticEgressResolver{resolved: egress.ResolvedEndpoint{ProxyURL: "://invalid"}})
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"account_id": "acct-123"}}
	req, _ := http.NewRequest(http.MethodGet, upstream.URL, nil)
	_, err := exec.HttpRequest(context.Background(), auth, req)
	if !errors.Is(err, egress.ErrEndpointInvalid) {
		t.Fatalf("HttpRequest() error = %v, want ErrEndpointInvalid", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("upstream direct hits = %d, want 0", hits.Load())
	}
}

func TestCodexWebsocketStrictEgressBlocksBeforeDial(t *testing.T) {
	t.Parallel()

	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, staticEgressResolver{err: egress.ErrEndpointDisabled})
	auth := &cliproxyauth.Auth{ID: "codex.json", Metadata: map[string]any{"account_id": "acct-123"}}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if !errors.Is(err, egress.ErrEndpointDisabled) {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestCodexWebsocketSessionRedialsWhenEgressRouteChanges(t *testing.T) {
	t.Parallel()

	wsURL, accepted, closeServer := newCodexSessionRouteTestServer(t)
	defer closeServer()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	sess := exec.getOrCreateSession("route-switch-session")

	connA, _, err := exec.ensureUpstreamConn(context.Background(), nil, sess, "auth-a", wsURL, "route-a", nil)
	if err != nil {
		t.Fatalf("ensure route A: %v", err)
	}
	waitForCodexWebsocketAccept(t, accepted)

	connB, _, err := exec.ensureUpstreamConn(context.Background(), nil, sess, "auth-a", wsURL, "route-b", nil)
	if err != nil {
		t.Fatalf("ensure route B: %v", err)
	}
	waitForCodexWebsocketAccept(t, accepted)
	if connA == connB {
		t.Fatal("route B reused route A websocket connection")
	}

	sess.connMu.Lock()
	gotRouteKey := sess.routeKey
	sess.connMu.Unlock()
	if gotRouteKey != "route-b" {
		t.Fatalf("session route key = %q, want route-b", gotRouteKey)
	}
	exec.CloseExecutionSession(sess.sessionID)
}

func TestCodexWebsocketSessionRedialsWhenAuthChanges(t *testing.T) {
	t.Parallel()

	wsURL, accepted, closeServer := newCodexSessionRouteTestServer(t)
	defer closeServer()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	sess := exec.getOrCreateSession("auth-switch-session")

	connA, _, err := exec.ensureUpstreamConn(context.Background(), nil, sess, "auth-a", wsURL, "route-a", nil)
	if err != nil {
		t.Fatalf("ensure auth A: %v", err)
	}
	waitForCodexWebsocketAccept(t, accepted)

	connB, _, err := exec.ensureUpstreamConn(context.Background(), nil, sess, "auth-b", wsURL, "route-a", nil)
	if err != nil {
		t.Fatalf("ensure auth B: %v", err)
	}
	waitForCodexWebsocketAccept(t, accepted)
	if connA == connB {
		t.Fatal("auth B reused auth A websocket connection")
	}

	exec.CloseExecutionSession(sess.sessionID)
}

func TestCodexWebsocketDisabledEgressDoesNotReuseExistingSession(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	received := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		for {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			received <- payload
		}
	}))
	defer upstream.Close()

	store := &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	seedExec := NewCodexWebsocketsExecutor(&config.Config{})
	seedExec.store = store
	sess := seedExec.getOrCreateSession("disabled-route-session")
	wsURL := "ws" + strings.TrimPrefix(upstream.URL, "http")
	if _, _, err := seedExec.ensureUpstreamConn(context.Background(), nil, sess, "auth-a", wsURL, "route-a", nil); err != nil {
		t.Fatalf("seed websocket session: %v", err)
	}

	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, staticEgressResolver{err: egress.ErrEndpointDisabled})
	exec.store = store
	auth := &cliproxyauth.Auth{
		ID:         "auth-a",
		Metadata:   map[string]any{"account_id": "acct-123"},
		Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL},
	}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sess.sessionID},
	})
	if !errors.Is(err, egress.ErrEndpointDisabled) {
		t.Fatalf("Execute() error = %v, want ErrEndpointDisabled", err)
	}
	select {
	case payload := <-received:
		t.Fatalf("disabled route sent request on stale websocket: %s", payload)
	case <-time.After(100 * time.Millisecond):
	}
	exec.CloseExecutionSession(sess.sessionID)
}

func TestCodexWebsocketRouteKeyDoesNotExposeProxyCredentials(t *testing.T) {
	t.Parallel()

	first := codexWebsocketRouteKey(&cliproxyauth.Auth{
		ProxyURL:   "http://relay-user:relay-secret@127.0.0.1:8080",
		Attributes: map[string]string{"egress_id": "endpoint-a"},
	})
	second := codexWebsocketRouteKey(&cliproxyauth.Auth{
		ProxyURL:   "http://relay-user:changed-secret@127.0.0.1:8080",
		Attributes: map[string]string{"egress_id": "endpoint-a"},
	})
	if strings.Contains(first, "relay-user") || strings.Contains(first, "relay-secret") {
		t.Fatalf("route key exposes proxy credentials: %q", first)
	}
	if first == second {
		t.Fatal("route key did not change when proxy credentials changed")
	}
}

func newCodexSessionRouteTestServer(t *testing.T) (string, <-chan struct{}, func()) {
	t.Helper()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	accepted := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		accepted <- struct{}{}
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	return "ws" + strings.TrimPrefix(server.URL, "http"), accepted, server.Close
}

func waitForCodexWebsocketAccept(t *testing.T, accepted <-chan struct{}) {
	t.Helper()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket connection")
	}
}

func TestCodexWebsocketStrictEgressUsesResolvedProxyNotEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	upgrader := websocket.Upgrader{}
	upstreamHit := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		upstreamHit <- struct{}{}
	}))
	defer upstream.Close()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	defer proxyListener.Close()
	proxyHit := make(chan struct{}, 1)
	proxyErr := make(chan error, 1)
	go func() {
		clientConn, errAccept := proxyListener.Accept()
		if errAccept != nil {
			proxyErr <- errAccept
			return
		}
		defer clientConn.Close()
		request, errRead := http.ReadRequest(bufio.NewReader(clientConn))
		if errRead != nil {
			proxyErr <- errRead
			return
		}
		if request.Method != http.MethodConnect {
			proxyErr <- errors.New("expected CONNECT")
			return
		}
		upstreamConn, errDial := net.Dial("tcp", request.Host)
		if errDial != nil {
			proxyErr <- errDial
			return
		}
		defer upstreamConn.Close()
		if _, errWrite := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
			proxyErr <- errWrite
			return
		}
		proxyHit <- struct{}{}
		go func() { _, _ = io.Copy(upstreamConn, clientConn) }()
		_, _ = io.Copy(clientConn, upstreamConn)
	}()

	resolver := staticEgressResolver{resolved: egress.ResolvedEndpoint{ProxyURL: "http://" + proxyListener.Addr().String()}}
	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, resolver)
	auth, err := exec.resolveEgressAuth(context.Background(), &cliproxyauth.Auth{Metadata: map[string]any{"account_id": "acct-123"}})
	if err != nil {
		t.Fatalf("resolveEgressAuth() error = %v", err)
	}
	wsURL, err := buildCodexResponsesWebsocketURL(upstream.URL)
	if err != nil {
		t.Fatalf("build websocket URL: %v", err)
	}
	conn, _, err := exec.dialCodexWebsocket(context.Background(), auth, wsURL, nil)
	if err != nil {
		select {
		case proxyFailure := <-proxyErr:
			t.Fatalf("dial websocket: %v (proxy: %v)", err, proxyFailure)
		default:
			t.Fatalf("dial websocket: %v", err)
		}
	}
	_ = conn.Close()
	select {
	case <-proxyHit:
	default:
		t.Fatal("resolved proxy was not used")
	}
	select {
	case <-upstreamHit:
	default:
		t.Fatal("upstream websocket was not reached through proxy")
	}
}
