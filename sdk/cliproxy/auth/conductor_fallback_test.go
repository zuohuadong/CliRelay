package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManagerExecute_EgressRuntimeErrorStopsCredentialFallback(t *testing.T) {
	const model = "gpt-5.3-codex"
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "codex",
		executeErrors: map[string]error{
			"aa-bound-auth": egress.RuntimeError(egress.ErrEndpointDisabled),
		},
	}
	m.RegisterExecutor(executor)
	registerFallbackAuths(t, m, model)

	_, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	var runtimeErr *egress.Error
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "egress_disabled" {
		t.Fatalf("Execute() error = %v, want egress_disabled", err)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 1 || calls[0] != "aa-bound-auth" {
		t.Fatalf("Execute() calls = %v, want only bound auth", calls)
	}
}

func TestManagerExecuteStream_EgressRuntimeErrorStopsCredentialFallback(t *testing.T) {
	const model = "gpt-5.3-codex"
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "codex",
		streamFirstErrors: map[string]error{
			"aa-bound-auth": egress.RuntimeError(egress.ErrEndpointDisabled),
		},
	}
	m.RegisterExecutor(executor)
	registerFallbackAuths(t, m, model)

	result, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatal("ExecuteStream() closed before error chunk")
	}
	var runtimeErr *egress.Error
	if !errors.As(chunk.Err, &runtimeErr) || runtimeErr.Code != "egress_disabled" {
		t.Fatalf("stream error = %v, want egress_disabled", chunk.Err)
	}
	if calls := executor.StreamCalls(); len(calls) != 1 || calls[0] != "aa-bound-auth" {
		t.Fatalf("ExecuteStream() calls = %v, want only bound auth", calls)
	}
}

func registerFallbackAuths(t *testing.T, manager *Manager, model string) {
	t.Helper()
	badAuth := &Auth{ID: "aa-bound-auth", Provider: "codex", Status: StatusActive}
	goodAuth := &Auth{ID: "bb-other-auth", Provider: "codex", Status: StatusActive}
	for _, auth := range []*Auth{badAuth, goodAuth} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(badAuth.ID)
		registry.GetGlobalRegistry().UnregisterClient(goodAuth.ID)
	})
}

type emptyThenPayloadStreamExecutor struct {
	id      string
	payload []byte
}

func (e *emptyThenPayloadStreamExecutor) Identifier() string { return e.id }

func (e *emptyThenPayloadStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *emptyThenPayloadStreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	if len(e.payload) > 0 {
		chunks <- cliproxyexecutor.StreamChunk{Payload: e.payload}
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *emptyThenPayloadStreamExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *emptyThenPayloadStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *emptyThenPayloadStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func TestShouldStopMixedProviderFallback_EmptyStreamRetryable(t *testing.T) {
	emptyStreamErr := &Error{
		Code:       "empty_stream",
		Message:    "upstream stream closed before first payload",
		Retryable:  true,
		HTTPStatus: 0,
	}
	// empty_stream is retryable, so fallback should NOT stop
	if shouldStopMixedProviderFallback("astron-code", "gpt-5.3-codex", emptyStreamErr) {
		t.Error("expected fallback to continue for retryable empty_stream error, but it stopped")
	}
}

func TestShouldStopMixedProviderFallback_500Stops(t *testing.T) {
	err500 := &Error{
		Code:       "upstream_error",
		Message:    "internal server error",
		Retryable:  false,
		HTTPStatus: http.StatusInternalServerError,
	}
	// non-retryable 500 should stop fallback
	if !shouldStopMixedProviderFallback("astron-code", "gpt-5.3-codex", err500) {
		t.Error("expected fallback to stop for non-retryable 500 error, but it continued")
	}
}

func TestShouldStopMixedProviderFallback_NonAstronProvider(t *testing.T) {
	err := &Error{
		Code:      "empty_stream",
		Message:   "upstream stream closed before first payload",
		Retryable: true,
	}
	if shouldStopMixedProviderFallback("bigmodel-coding", "gpt-5.3-codex", err) {
		t.Error("expected fallback to continue for non-astron-code provider, but it stopped")
	}
}

func TestShouldStopMixedProviderFallback_NonCodexModel(t *testing.T) {
	err := &Error{
		Code:      "empty_stream",
		Message:   "upstream stream closed before first payload",
		Retryable: true,
	}
	if shouldStopMixedProviderFallback("astron-code", "gpt-4o", err) {
		t.Error("expected fallback to continue for non-codex model, but it stopped")
	}
}

func TestShouldStopMixedProviderFallback_Retryable502(t *testing.T) {
	err := &Error{
		Code:       "bad_gateway",
		Message:    "bad gateway",
		Retryable:  true,
		HTTPStatus: http.StatusBadGateway,
	}
	// Retryable 502 should continue fallback
	if shouldStopMixedProviderFallback("astron-code", "gpt-5.3-codex", err) {
		t.Error("expected fallback to continue for retryable 502 error, but it stopped")
	}
}

func TestShouldStopMixedProviderFallback_NonRetryable502(t *testing.T) {
	err := &Error{
		Code:       "bad_gateway",
		Message:    "bad gateway",
		Retryable:  false,
		HTTPStatus: http.StatusBadGateway,
	}
	// Non-retryable 502 should stop fallback
	if !shouldStopMixedProviderFallback("astron-code", "gpt-5.3-codex", err) {
		t.Error("expected fallback to stop for non-retryable 502 error, but it continued")
	}
}

func TestManagerExecuteStream_DownstreamWebsocketAstronEmptyStreamFallsBack(t *testing.T) {
	const model = "gpt-5.3-codex"
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(&emptyThenPayloadStreamExecutor{id: "astron-code"})
	m.RegisterExecutor(&emptyThenPayloadStreamExecutor{id: "bigmodel-coding", payload: []byte("bigmodel-ok")})

	astronAuth := &Auth{ID: "astron-auth", Provider: "astron-code", Status: StatusActive}
	bigModelAuth := &Auth{ID: "bigmodel-auth", Provider: "bigmodel-coding", Status: StatusActive}
	if _, err := m.Register(context.Background(), astronAuth); err != nil {
		t.Fatalf("register astron auth: %v", err)
	}
	if _, err := m.Register(context.Background(), bigModelAuth); err != nil {
		t.Fatalf("register bigmodel auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(astronAuth.ID, astronAuth.Provider, []*registry.ModelInfo{{ID: model}})
	registry.GetGlobalRegistry().RegisterClient(bigModelAuth.ID, bigModelAuth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(astronAuth.ID)
		registry.GetGlobalRegistry().UnregisterClient(bigModelAuth.ID)
	})

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	result, err := m.ExecuteStream(ctx, []string{"astron-code", "bigmodel-coding"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	if result == nil || result.Chunks == nil {
		t.Fatalf("expected stream result with chunks")
	}
	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatalf("stream closed before fallback payload")
	}
	if chunk.Err != nil {
		t.Fatalf("fallback chunk error: %v", chunk.Err)
	}
	if string(chunk.Payload) != "bigmodel-ok" {
		t.Fatalf("payload = %q, want bigmodel-ok", string(chunk.Payload))
	}
}
