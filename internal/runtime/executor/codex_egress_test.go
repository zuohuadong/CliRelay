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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexStrictEgressRefreshRejectsAccountIdentityDriftBeforeTokenMutation(t *testing.T) {
	t.Parallel()

	exec := NewCodexExecutorWithEgress(
		&config.Config{},
		staticEgressResolver{resolved: egress.ResolvedEndpoint{Endpoint: egress.Endpoint{ID: "endpoint-a"}, ProxyURL: "socks5://10.77.0.2:1080"}},
	)
	exec.refreshTokens = func(context.Context, *http.Client, string) (*codexauth.CodexTokenData, error) {
		return &codexauth.CodexTokenData{
			AccountID:    "acct-other",
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			IDToken:      "new-id-token",
		}, nil
	}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"account_id":    "acct-bound",
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"id_token":      "old-id-token",
	}}

	updated, err := exec.Refresh(context.Background(), auth)
	if !errors.Is(err, egress.ErrIdentityMismatch) {
		t.Fatalf("Refresh() error = %v, want ErrIdentityMismatch", err)
	}
	if updated != nil {
		t.Fatalf("Refresh() updated auth = %#v, want nil", updated)
	}
	if got := auth.Metadata["account_id"]; got != "acct-bound" {
		t.Fatalf("account_id mutated to %v", got)
	}
	if got := auth.Metadata["access_token"]; got != "old-access" {
		t.Fatalf("access_token mutated to %v", got)
	}
	if got := auth.Metadata["refresh_token"]; got != "old-refresh" {
		t.Fatalf("refresh_token mutated to %v", got)
	}
}

type staticEgressResolver struct {
	resolved egress.ResolvedEndpoint
	err      error
}

type recordingEgressResolver struct {
	mu       sync.Mutex
	resolved egress.ResolvedEndpoint
	accounts []string
}

func (r *recordingEgressResolver) Resolve(_ context.Context, accountID string) (egress.ResolvedEndpoint, error) {
	r.mu.Lock()
	r.accounts = append(r.accounts, accountID)
	r.mu.Unlock()
	return r.resolved, nil
}

func (r *recordingEgressResolver) Accounts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.accounts...)
}

func TestCodexStrictEgressResolveInjectsCurrentEndpointOnRuntimeClone(t *testing.T) {
	t.Parallel()

	exec := NewCodexExecutorWithEgress(
		&config.Config{},
		staticEgressResolver{resolved: egress.ResolvedEndpoint{
			Endpoint: egress.Endpoint{ID: "endpoint-current"},
			ProxyURL: "socks5://10.77.0.2:1080",
		}},
	)
	original := &cliproxyauth.Auth{
		ProxyURL:   "http://legacy.invalid:8080",
		Metadata:   map[string]any{"account_id": "acct-123"},
		Attributes: map[string]string{"egress_id": "endpoint-legacy"},
	}

	resolved, err := exec.resolveEgressAuth(context.Background(), original)
	if err != nil {
		t.Fatalf("resolveEgressAuth() error = %v", err)
	}
	if resolved == original {
		t.Fatal("resolveEgressAuth() returned the persisted auth instead of a runtime clone")
	}
	if got, want := resolved.ProxyURL, "socks5://10.77.0.2:1080"; got != want {
		t.Fatalf("runtime proxy URL = %q, want %q", got, want)
	}
	if got, want := resolved.Attributes["egress_id"], "endpoint-current"; got != want {
		t.Fatalf("runtime egress_id = %q, want %q", got, want)
	}
	if got := original.ProxyURL; got != "http://legacy.invalid:8080" {
		t.Fatalf("persisted auth proxy URL mutated to %q", got)
	}
	if got := original.Attributes["egress_id"]; got != "endpoint-legacy" {
		t.Fatalf("persisted auth egress_id mutated to %q", got)
	}
}

func TestCodexStrictEgressHttpRequestWrapsProxyDialFailure(t *testing.T) {
	t.Parallel()

	exec := NewCodexExecutorWithEgress(
		&config.Config{},
		staticEgressResolver{resolved: egress.ResolvedEndpoint{Endpoint: egress.Endpoint{ID: "endpoint-a"}, ProxyURL: closedProxyURL(t)}},
	)
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"account_id": "acct-123"}}
	req, err := http.NewRequest(http.MethodGet, "http://upstream.invalid/test", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = exec.HttpRequest(context.Background(), auth, req)
	var runtimeErr *egress.Error
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "egress_disabled" {
		t.Fatalf("HttpRequest() error = %v, want egress_disabled RuntimeError", err)
	}
}

func TestCodexSharedProxyHttpBypassesStrictEndpointResolver(t *testing.T) {
	t.Parallel()

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if got, want := r.URL.String(), "http://upstream.invalid/shared"; got != want {
			t.Fatalf("proxy URL = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte("shared-proxy"))
	}))
	defer proxy.Close()

	resolver := &recordingEgressResolver{resolved: egress.ResolvedEndpoint{
		Endpoint: egress.Endpoint{ID: "strict-endpoint"},
		ProxyURL: closedProxyURL(t),
	}}
	exec := NewCodexExecutorWithEgress(&config.Config{}, resolver)
	auth := &cliproxyauth.Auth{
		ProxyURL: proxy.URL,
		Metadata: map[string]any{
			"account_id":  "acct-shared",
			"egress_mode": "shared_proxy",
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://upstream.invalid/shared", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := exec.HttpRequest(context.Background(), auth, req)
	if err != nil {
		t.Fatalf("HttpRequest() error = %v", err)
	}
	defer resp.Body.Close()
	if got := proxyHits.Load(); got != 1 {
		t.Fatalf("shared proxy hits = %d, want 1", got)
	}
	if accounts := resolver.Accounts(); len(accounts) != 0 {
		t.Fatalf("strict egress resolver accounts = %v, want none", accounts)
	}
}

func TestCodexSharedProxyUsesGlobalProxyWhenAuthProxyIsEmpty(t *testing.T) {
	t.Parallel()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.String(), "http://upstream.invalid/global"; got != want {
			t.Fatalf("proxy URL = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte("global-shared-proxy"))
	}))
	defer proxy.Close()

	resolver := &recordingEgressResolver{resolved: egress.ResolvedEndpoint{
		Endpoint: egress.Endpoint{ID: "strict-endpoint"},
		ProxyURL: closedProxyURL(t),
	}}
	exec := NewCodexExecutorWithEgress(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: proxy.URL}}, resolver)
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"account_id":  "acct-shared-global",
		"egress_mode": "shared_proxy",
	}}
	req, err := http.NewRequest(http.MethodGet, "http://upstream.invalid/global", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := exec.HttpRequest(context.Background(), auth, req)
	if err != nil {
		t.Fatalf("HttpRequest() error = %v", err)
	}
	defer resp.Body.Close()
	if accounts := resolver.Accounts(); len(accounts) != 0 {
		t.Fatalf("strict egress resolver accounts = %v, want none", accounts)
	}
}

func TestCodexSharedProxyRefreshUsesHomeWithoutResolvingStrictEndpoint(t *testing.T) {
	t.Parallel()

	resolver := &recordingEgressResolver{resolved: egress.ResolvedEndpoint{
		Endpoint: egress.Endpoint{ID: "strict-endpoint"},
		ProxyURL: closedProxyURL(t),
	}}
	exec := NewCodexExecutorWithEgress(&config.Config{Home: config.HomeConfig{Enabled: true}}, resolver)
	refreshed := &cliproxyauth.Auth{ID: "shared-refreshed", Provider: "codex"}
	var homeRefreshCalls atomic.Int32
	exec.homeRefresh = func(_ context.Context, _ *config.Config, got *cliproxyauth.Auth) (*cliproxyauth.Auth, bool, error) {
		homeRefreshCalls.Add(1)
		if got == nil || got.Metadata["egress_mode"] != "shared_proxy" {
			t.Fatalf("home refresh auth = %#v, want shared proxy auth", got)
		}
		return refreshed, true, nil
	}
	auth := &cliproxyauth.Auth{
		ProxyURL: "http://configured-proxy.invalid:8080",
		Metadata: map[string]any{
			"account_id":  "acct-shared",
			"egress_mode": "shared_proxy",
		},
	}

	got, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got != refreshed {
		t.Fatalf("Refresh() auth = %#v, want home-refreshed auth", got)
	}
	if got := homeRefreshCalls.Load(); got != 1 {
		t.Fatalf("home refresh calls = %d, want 1", got)
	}
	if accounts := resolver.Accounts(); len(accounts) != 0 {
		t.Fatalf("strict egress resolver accounts = %v, want none", accounts)
	}
}

func TestCodexSharedProxyRejectsMissingInvalidAndDirectProxyBeforeEndpointResolve(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		cfgProxy  string
		authProxy string
		want      error
	}{
		{name: "missing", want: egress.ErrEgressRequired},
		{name: "invalid", authProxy: "://invalid", want: egress.ErrEndpointInvalid},
		{name: "direct", cfgProxy: "http://configured-proxy.invalid:8080", authProxy: "direct", want: egress.ErrEndpointInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &recordingEgressResolver{resolved: egress.ResolvedEndpoint{
				Endpoint: egress.Endpoint{ID: "strict-endpoint"},
				ProxyURL: closedProxyURL(t),
			}}
			exec := NewCodexExecutorWithEgress(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: tc.cfgProxy}}, resolver)
			_, err := exec.resolveEgressAuth(context.Background(), &cliproxyauth.Auth{
				ProxyURL: tc.authProxy,
				Metadata: map[string]any{
					"account_id":  "acct-shared",
					"egress_mode": "shared_proxy",
				},
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("resolveEgressAuth() error = %v, want %v", err, tc.want)
			}
			if accounts := resolver.Accounts(); len(accounts) != 0 {
				t.Fatalf("strict egress resolver accounts = %v, want none", accounts)
			}
		})
	}
}

func TestCodexWebsocketSharedProxyUsesNormalReadErrorClassification(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()
	proxy := newCodexHTTPConnectProxy(t)
	defer proxy.Close()

	resolver := &recordingEgressResolver{resolved: egress.ResolvedEndpoint{
		Endpoint: egress.Endpoint{ID: "strict-endpoint"},
		ProxyURL: closedProxyURL(t),
	}}
	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, resolver)
	auth := &cliproxyauth.Auth{
		ProxyURL: proxy.URL,
		Metadata: map[string]any{
			"account_id":   "acct-shared",
			"access_token": "token",
			"egress_mode":  "shared_proxy",
		},
		Attributes: map[string]string{"base_url": upstream.URL},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err == nil {
		t.Fatal("Execute() error = nil, want normal upstream disconnect error")
	}
	var runtimeErr *egress.Error
	if errors.As(err, &runtimeErr) {
		t.Fatalf("Execute() error = %v, want normal websocket error instead of egress runtime error", err)
	}
	var statusError interface{ StatusCode() int }
	if !errors.As(err, &statusError) || statusError.StatusCode() != http.StatusRequestTimeout {
		t.Fatalf("Execute() error = %v, want replayable websocket disconnect status", err)
	}
	if accounts := resolver.Accounts(); len(accounts) != 0 {
		t.Fatalf("strict egress resolver accounts = %v, want none", accounts)
	}
}

func TestCodexStrictEgressTransportErrorDoesNotExposeProxyCredentials(t *testing.T) {
	t.Parallel()

	proxyURL := "http://relay-user:relay-secret@" + strings.TrimPrefix(closedProxyURL(t), "http://")
	exec := NewCodexExecutorWithEgress(
		&config.Config{},
		staticEgressResolver{resolved: egress.ResolvedEndpoint{Endpoint: egress.Endpoint{ID: "endpoint-a"}, ProxyURL: proxyURL}},
	)
	req, err := http.NewRequest(http.MethodGet, "http://upstream.invalid/test", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = exec.HttpRequest(context.Background(), &cliproxyauth.Auth{Metadata: map[string]any{"account_id": "acct-123"}}, req)
	assertEgressRuntimeError(t, err)
	if message := err.Error(); strings.Contains(message, "relay-user") || strings.Contains(message, "relay-secret") {
		t.Fatalf("strict egress transport error exposes proxy credentials: %q", message)
	}
}

func TestCodexStrictEgressProxyDialFailuresAreRuntimeErrors(t *testing.T) {
	t.Parallel()

	proxyURL := closedProxyURL(t)
	newExecutor := func() *CodexExecutor {
		return NewCodexExecutorWithEgress(
			&config.Config{},
			staticEgressResolver{resolved: egress.ResolvedEndpoint{Endpoint: egress.Endpoint{ID: "endpoint-a"}, ProxyURL: proxyURL}},
		)
	}
	newAuth := func() *cliproxyauth.Auth {
		return &cliproxyauth.Auth{
			Metadata:   map[string]any{"account_id": "acct-123", "access_token": "token", "refresh_token": "refresh"},
			Attributes: map[string]string{"base_url": "https://upstream.invalid"},
		}
	}
	request := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}

	t.Run("execute", func(t *testing.T) {
		_, err := newExecutor().Execute(context.Background(), newAuth(), request, opts)
		assertEgressRuntimeError(t, err)
	})
	t.Run("stream immediate", func(t *testing.T) {
		_, err := newExecutor().ExecuteStream(context.Background(), newAuth(), request, opts)
		assertEgressRuntimeError(t, err)
	})
	t.Run("refresh", func(t *testing.T) {
		exec := newExecutor()
		exec.refreshTokens = func(ctx context.Context, client *http.Client, _ string) (*codexauth.CodexTokenData, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://auth.openai.com/oauth/token", strings.NewReader("grant_type=refresh_token"))
			if err != nil {
				return nil, err
			}
			_, err = client.Do(req)
			return nil, err
		}
		_, err := exec.Refresh(context.Background(), newAuth())
		assertEgressRuntimeError(t, err)
	})
	t.Run("quota", func(t *testing.T) {
		_, err := newExecutor().ProbeQuotaRecovery(context.Background(), newAuth())
		assertEgressRuntimeError(t, err)
	})
	t.Run("compact", func(t *testing.T) {
		_, err := newExecutor().Execute(context.Background(), newAuth(), request, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Alt:          "responses/compact",
		})
		assertEgressRuntimeError(t, err)
	})
	imageRequest := cliproxyexecutor.Request{Model: "gpt-image-2", Payload: []byte(`{"model":"gpt-image-2","prompt":"cat"}`)}
	imageOpts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString(codexOpenAIImageSourceFormat),
		Metadata:     map[string]any{cliproxyexecutor.RequestPathMetadataKey: codexImagesGenerationsPath},
	}
	t.Run("image", func(t *testing.T) {
		_, err := newExecutor().Execute(context.Background(), newAuth(), imageRequest, imageOpts)
		assertEgressRuntimeError(t, err)
	})
	t.Run("image stream", func(t *testing.T) {
		_, err := newExecutor().ExecuteStream(context.Background(), newAuth(), imageRequest, imageOpts)
		assertEgressRuntimeError(t, err)
	})
}

func TestCodexWebsocketStrictEgressProxyDialFailuresAreRuntimeErrors(t *testing.T) {
	proxyURL := closedProxyURL(t)
	newExecutor := func() *CodexWebsocketsExecutor {
		return NewCodexWebsocketsExecutorWithEgress(
			&config.Config{},
			staticEgressResolver{resolved: egress.ResolvedEndpoint{Endpoint: egress.Endpoint{ID: "endpoint-a"}, ProxyURL: proxyURL}},
		)
	}
	auth := &cliproxyauth.Auth{
		Metadata:   map[string]any{"account_id": "acct-123", "access_token": "token"},
		Attributes: map[string]string{"base_url": "https://upstream.invalid"},
	}
	request := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}

	t.Run("execute", func(t *testing.T) {
		_, err := newExecutor().Execute(context.Background(), auth, request, opts)
		assertEgressRuntimeError(t, err)
	})
	t.Run("stream", func(t *testing.T) {
		_, err := newExecutor().ExecuteStream(context.Background(), auth, request, opts)
		assertEgressRuntimeError(t, err)
	})
}

func TestCodexWebsocketStrictEgressWrapsTransportFailureBeforeFirstMessage(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()
	proxy := newCodexHTTPConnectProxy(t)
	defer proxy.Close()

	resolver := staticEgressResolver{resolved: egress.ResolvedEndpoint{
		Endpoint: egress.Endpoint{ID: "endpoint-a"},
		ProxyURL: proxy.URL,
	}}
	auth := &cliproxyauth.Auth{
		Metadata:   map[string]any{"account_id": "acct-123", "access_token": "token"},
		Attributes: map[string]string{"base_url": upstream.URL},
	}
	request := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}

	t.Run("execute", func(t *testing.T) {
		exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, resolver)
		_, err := exec.Execute(context.Background(), auth, request, opts)
		assertEgressRuntimeError(t, err)
	})
	t.Run("stream", func(t *testing.T) {
		exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, resolver)
		result, err := exec.ExecuteStream(context.Background(), auth, request, opts)
		if err != nil {
			assertEgressRuntimeError(t, err)
			return
		}
		if result == nil {
			t.Fatal("ExecuteStream() result = nil")
		}
		chunk, ok := <-result.Chunks
		if !ok {
			t.Fatal("stream closed without transport error")
		}
		assertEgressRuntimeError(t, chunk.Err)
	})
}

func newCodexHTTPConnectProxy(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		upstream, err := net.DialTimeout("tcp", r.Host, 2*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			_ = upstream.Close()
			http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
			return
		}
		client, buffer, err := hijacker.Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
		if err = buffer.Flush(); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
		go func() {
			defer client.Close()
			defer upstream.Close()
			_, _ = io.Copy(upstream, client)
		}()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		_ = upstream.Close()
	}))
}

func TestCodexStrictEgressManagerFallsBackAfterProxyDialFailure(t *testing.T) {
	for _, stream := range []bool{false, true} {
		stream := stream
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			const model = "gpt-5.4"
			resolver := &recordingEgressResolver{resolved: egress.ResolvedEndpoint{
				Endpoint: egress.Endpoint{ID: "endpoint-a"},
				ProxyURL: closedProxyURL(t),
			}}
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(NewCodexExecutorWithEgress(&config.Config{}, resolver))
			for _, auth := range []*cliproxyauth.Auth{
				{ID: "aa-bound-auth", Provider: "codex", Status: cliproxyauth.StatusActive, Metadata: map[string]any{"account_id": "acct-a", "access_token": "token-a"}, Attributes: map[string]string{"base_url": "https://upstream.invalid"}},
				{ID: "bb-other-auth", Provider: "codex", Status: cliproxyauth.StatusActive, Metadata: map[string]any{"account_id": "acct-b", "access_token": "token-b"}, Attributes: map[string]string{"base_url": "https://upstream.invalid"}},
			} {
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatalf("register %s: %v", auth.ID, err)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}
			req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
			var err error
			if stream {
				_, err = manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
			} else {
				_, err = manager.Execute(context.Background(), []string{"codex"}, req, opts)
			}
			assertEgressRuntimeError(t, err)
			if accounts := resolver.Accounts(); len(accounts) != 2 || accounts[0] != "acct-a" || accounts[1] != "acct-b" {
				t.Fatalf("resolved accounts = %v, want acct-a followed by acct-b", accounts)
			}
		})
	}
}

func TestCodexStrictEgressManagerFallsBackAfterFirstResponseBodyFailure(t *testing.T) {
	const model = "gpt-5.4"
	proxy := newCodexTruncatedResponseProxy(t)
	defer proxy.Close()
	resolver := &recordingEgressResolver{resolved: egress.ResolvedEndpoint{
		Endpoint: egress.Endpoint{ID: "endpoint-a"},
		ProxyURL: proxy.URL,
	}}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutorWithEgress(&config.Config{}, resolver))
	for _, auth := range []*cliproxyauth.Auth{
		{ID: "aa-bound-auth", Provider: "codex", Status: cliproxyauth.StatusActive, Metadata: map[string]any{"account_id": "acct-a", "access_token": "token-a"}, Attributes: map[string]string{"base_url": "http://upstream.invalid"}},
		{ID: "bb-other-auth", Provider: "codex", Status: cliproxyauth.StatusActive, Metadata: map[string]any{"account_id": "acct-b", "access_token": "token-b"}, Attributes: map[string]string{"base_url": "http://upstream.invalid"}},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model: model, Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	assertEgressRuntimeError(t, err)
	if accounts := resolver.Accounts(); len(accounts) != 2 || accounts[0] != "acct-a" || accounts[1] != "acct-b" {
		t.Fatalf("resolved accounts = %v, want acct-a followed by acct-b", accounts)
	}
}

func TestCodexStrictEgressWrapsStreamBodyReadFailureBeforeFirstPayload(t *testing.T) {
	t.Parallel()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "128")
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	exec := NewCodexExecutorWithEgress(
		&config.Config{},
		staticEgressResolver{resolved: egress.ResolvedEndpoint{Endpoint: egress.Endpoint{ID: "endpoint-a"}, ProxyURL: proxy.URL}},
	)
	auth := &cliproxyauth.Auth{
		Metadata:   map[string]any{"account_id": "acct-123", "access_token": "token"},
		Attributes: map[string]string{"base_url": "http://upstream.invalid"},
	}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var readErr error
	for chunk := range result.Chunks {
		if len(chunk.Payload) > 0 {
			t.Fatalf("unexpected payload before read failure: %q", chunk.Payload)
		}
		if chunk.Err != nil {
			readErr = chunk.Err
			break
		}
	}
	assertEgressRuntimeError(t, readErr)
}

func TestCodexStrictEgressResponseHeaderTimeoutRemainsRetryableGatewayTimeout(t *testing.T) {
	release := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer proxy.Close()

	exec := NewCodexExecutorWithEgress(
		&config.Config{Codex: config.CodexConfig{ResponseHeaderTimeoutSeconds: 1}},
		staticEgressResolver{resolved: egress.ResolvedEndpoint{Endpoint: egress.Endpoint{ID: "endpoint-a"}, ProxyURL: proxy.URL}},
	)
	auth := &cliproxyauth.Auth{
		Metadata:   map[string]any{"account_id": "acct-123", "access_token": "token"},
		Attributes: map[string]string{"base_url": "http://upstream.invalid"},
	}
	_, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	close(release)
	if err == nil {
		t.Fatal("ExecuteStream() error = nil, want response header timeout")
	}
	var runtimeErr *egress.Error
	if errors.As(err, &runtimeErr) {
		t.Fatalf("ExecuteStream() error = %v, must not become terminal strict-egress error", err)
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusGatewayTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusGatewayTimeout, err)
	}
}

func TestCodexStrictEgressWrapsNonStreamBodyReadFailures(t *testing.T) {
	proxy := newCodexTruncatedResponseProxy(t)
	defer proxy.Close()
	newExecutor := func() *CodexExecutor {
		return NewCodexExecutorWithEgress(
			&config.Config{},
			staticEgressResolver{resolved: egress.ResolvedEndpoint{Endpoint: egress.Endpoint{ID: "endpoint-a"}, ProxyURL: proxy.URL}},
		)
	}
	auth := func() *cliproxyauth.Auth {
		return &cliproxyauth.Auth{
			Metadata:   map[string]any{"account_id": "acct-123", "access_token": "token"},
			Attributes: map[string]string{"base_url": "http://upstream.invalid"},
		}
	}

	t.Run("responses", func(t *testing.T) {
		_, err := newExecutor().Execute(context.Background(), auth(), cliproxyexecutor.Request{
			Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
		assertEgressRuntimeError(t, err)
	})
	t.Run("compact", func(t *testing.T) {
		_, err := newExecutor().Execute(context.Background(), auth(), cliproxyexecutor.Request{
			Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hi"}`),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Alt: "responses/compact"})
		assertEgressRuntimeError(t, err)
	})
	t.Run("image", func(t *testing.T) {
		_, err := newExecutor().Execute(context.Background(), auth(), cliproxyexecutor.Request{
			Model: "gpt-image-2", Payload: []byte(`{"model":"gpt-image-2","prompt":"cat"}`),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString(codexOpenAIImageSourceFormat),
			Metadata:     map[string]any{cliproxyexecutor.RequestPathMetadataKey: codexImagesGenerationsPath},
		})
		assertEgressRuntimeError(t, err)
	})
}

func TestCodexStrictEgressWrapsImageStreamBodyReadFailureBeforeFirstPayload(t *testing.T) {
	proxy := newCodexTruncatedResponseProxy(t)
	defer proxy.Close()
	exec := NewCodexExecutorWithEgress(
		&config.Config{},
		staticEgressResolver{resolved: egress.ResolvedEndpoint{Endpoint: egress.Endpoint{ID: "endpoint-a"}, ProxyURL: proxy.URL}},
	)
	auth := &cliproxyauth.Auth{
		Metadata:   map[string]any{"account_id": "acct-123", "access_token": "token"},
		Attributes: map[string]string{"base_url": "http://upstream.invalid"},
	}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-image-2", Payload: []byte(`{"model":"gpt-image-2","prompt":"cat"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString(codexOpenAIImageSourceFormat),
		Metadata:     map[string]any{cliproxyexecutor.RequestPathMetadataKey: codexImagesGenerationsPath},
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatal("stream closed without body read error")
	}
	if len(chunk.Payload) > 0 {
		t.Fatalf("unexpected payload before read failure: %q", chunk.Payload)
	}
	assertEgressRuntimeError(t, chunk.Err)
}

func newCodexTruncatedResponseProxy(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "128")
		w.WriteHeader(http.StatusOK)
	}))
}

func assertEgressRuntimeError(t *testing.T, err error) {
	t.Helper()
	var runtimeErr *egress.Error
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "egress_disabled" {
		t.Fatalf("error = %v, want egress_disabled RuntimeError", err)
	}
}

func closedProxyURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen closed proxy address: %v", err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatalf("close proxy listener: %v", err)
	}
	return "http://" + address
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

func TestCodexStrictEgressMissingAccountIDReturnsCredentialLocalError(t *testing.T) {
	t.Parallel()

	resolver := &recordingEgressResolver{resolved: egress.ResolvedEndpoint{
		Endpoint: egress.Endpoint{ID: "must-not-resolve"},
		ProxyURL: "socks5://127.0.0.1:1080",
	}}
	exec := NewCodexExecutorWithEgress(&config.Config{}, resolver)

	_, err := exec.resolveEgressAuth(context.Background(), &cliproxyauth.Auth{Metadata: map[string]any{}})
	var runtimeErr *egress.Error
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "egress_identity_required" {
		t.Fatalf("resolveEgressAuth() error = %v, want egress_identity_required", err)
	}
	if accounts := resolver.Accounts(); len(accounts) != 0 {
		t.Fatalf("resolver accounts = %v, want no lookup without an account identity", accounts)
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

func TestCodexWebsocketStrictEgressWrapsProxyDialFailure(t *testing.T) {
	t.Parallel()

	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, staticEgressResolver{})
	auth := &cliproxyauth.Auth{ProxyURL: closedProxyURL(t)}
	_, _, err := exec.dialCodexWebsocket(context.Background(), auth, "wss://upstream.invalid/backend-api/codex/responses", nil)
	assertEgressRuntimeError(t, err)
}

func TestCodexWebsocketStrictEgressRejectsHTTPSProxy(t *testing.T) {
	t.Parallel()

	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, staticEgressResolver{})
	auth := &cliproxyauth.Auth{ProxyURL: "https://127.0.0.1:8443"}
	_, _, err := exec.dialCodexWebsocket(context.Background(), auth, "wss://upstream.invalid/backend-api/codex/responses", nil)
	if !errors.Is(err, egress.ErrEndpointInvalid) {
		t.Fatalf("dialCodexWebsocket() error = %v, want ErrEndpointInvalid", err)
	}
}

func TestCodexWebsocketStrictEgressWrapsProxyConnectServiceUnavailable(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
	}))
	defer proxy.Close()
	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, staticEgressResolver{})
	auth := &cliproxyauth.Auth{ProxyURL: proxy.URL}
	_, _, err := exec.dialCodexWebsocket(context.Background(), auth, "wss://upstream.invalid/backend-api/codex/responses", nil)
	assertEgressRuntimeError(t, err)
}

func TestCodexWebsocketStrictEgressPreservesUpstreamHandshakeFailure(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	proxy := newCodexHTTPConnectProxy(t)
	defer proxy.Close()
	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, staticEgressResolver{})
	auth := &cliproxyauth.Auth{ProxyURL: proxy.URL}
	wsURL, err := buildCodexResponsesWebsocketURL(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	_, resp, err := exec.dialCodexWebsocket(context.Background(), auth, wsURL, nil)
	if err == nil {
		t.Fatal("dialCodexWebsocket() error = nil, want upstream handshake failure")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("dialCodexWebsocket() response = %#v, want upstream 503", resp)
	}
	var runtimeErr *egress.Error
	if errors.As(err, &runtimeErr) {
		t.Fatalf("upstream handshake failure misclassified as egress runtime error: %v", err)
	}
}

func TestCodexWebsocketStrictEgressWrapsWriteFailure(t *testing.T) {
	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, staticEgressResolver{})
	err := exec.writeCodexWebsocketMessage(nil, nil, []byte(`{"type":"response.create"}`))
	assertEgressRuntimeError(t, err)
}

func TestCodexWebsocketStrictEgressWrapsPreResponseReadFailure(t *testing.T) {
	t.Parallel()

	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, staticEgressResolver{})
	err := exec.mapWebsocketReadError(io.ErrUnexpectedEOF)
	assertEgressRuntimeError(t, err)
}

func TestCodexWebsocketStrictEgressWrapsReadEOF(t *testing.T) {
	t.Parallel()

	exec := NewCodexWebsocketsExecutorWithEgress(&config.Config{}, staticEgressResolver{})
	err := exec.mapWebsocketReadError(io.EOF)
	assertEgressRuntimeError(t, err)
}

func TestCodexAccountIDFromAuthFallsBackToJWT(t *testing.T) {
	t.Parallel()

	// Case 1: account_id in metadata — use it directly
	auth1 := &cliproxyauth.Auth{Metadata: map[string]any{
		"account_id": "acct-direct",
		"id_token":   "eyJhbGciOiAiUlMyNTYiLCAidHlwIjogIkpXVCJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOiB7ImNoYXRncHRfYWNjb3VudF9pZCI6ICJhY2N0LWZyb20tand0In19.ZmFrZXNpZw",
	}}
	if got := codexAccountIDFromAuth(auth1); got != "acct-direct" {
		t.Fatalf("case 1: got %q, want acct-direct", got)
	}

	// Case 2: no account_id but id_token has chatgpt_account_id
	auth2 := &cliproxyauth.Auth{Metadata: map[string]any{
		"id_token": "eyJhbGciOiAiUlMyNTYiLCAidHlwIjogIkpXVCJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOiB7ImNoYXRncHRfYWNjb3VudF9pZCI6ICJhY2N0LWZyb20tand0In19.ZmFrZXNpZw",
	}}
	if got := codexAccountIDFromAuth(auth2); got != "acct-from-jwt" {
		t.Fatalf("case 2: got %q, want acct-from-jwt", got)
	}
	// Should also backfill metadata
	if got := auth2.Metadata["account_id"]; got != "acct-from-jwt" {
		t.Fatalf("case 2 backfill: got %v, want acct-from-jwt", got)
	}

	// Case 3: nil auth
	if got := codexAccountIDFromAuth(nil); got != "" {
		t.Fatalf("case 3: got %q, want empty", got)
	}

	// Case 4: no metadata at all
	auth4 := &cliproxyauth.Auth{}
	if got := codexAccountIDFromAuth(auth4); got != "" {
		t.Fatalf("case 4: got %q, want empty", got)
	}

	// Case 5: empty account_id, no id_token
	auth5 := &cliproxyauth.Auth{Metadata: map[string]any{
		"account_id": "",
	}}
	if got := codexAccountIDFromAuth(auth5); got != "" {
		t.Fatalf("case 5: got %q, want empty", got)
	}
}
