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

func TestManagerExecute_EgressDisabledFallsBackToNextCredential(t *testing.T) {
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

	resp, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "bb-other-auth" {
		t.Fatalf("Execute() payload = %q, want next healthy auth", got)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 2 || calls[0] != "aa-bound-auth" || calls[1] != "bb-other-auth" {
		t.Fatalf("Execute() calls = %v, want failed auth followed by next auth", calls)
	}
}

func TestManagerExecute_EgressUnboundFallsBackToNextCredential(t *testing.T) {
	const model = "gpt-5.3-codex"
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "codex",
		executeErrors: map[string]error{
			"aa-bound-auth": egress.RuntimeError(egress.ErrEgressUnbound),
		},
	}
	m.RegisterExecutor(executor)
	registerFallbackAuths(t, m, model)

	resp, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "bb-other-auth" {
		t.Fatalf("Execute() payload = %q, want next healthy auth", got)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 2 || calls[0] != "aa-bound-auth" || calls[1] != "bb-other-auth" {
		t.Fatalf("Execute() calls = %v, want failed auth followed by next auth", calls)
	}
}

func TestManagerExecute_EgressDisabledDuringPrepareFallsBackToNextCredential(t *testing.T) {
	const model = "gpt-5.3-codex"
	m := NewManager(nil, nil, nil)
	executor := &egressPrepareFallbackExecutor{
		authFallbackExecutor: &authFallbackExecutor{id: "codex"},
		prepareErrors: map[string]error{
			"aa-bound-auth": egress.RuntimeError(egress.ErrEndpointDisabled),
		},
	}
	m.RegisterExecutor(executor)
	registerFallbackAuths(t, m, model)

	resp, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "bb-other-auth" {
		t.Fatalf("Execute() payload = %q, want next healthy auth", got)
	}
	if calls := executor.PrepareCalls(); len(calls) != 2 || calls[0] != "aa-bound-auth" || calls[1] != "bb-other-auth" {
		t.Fatalf("PrepareRequestAuth() calls = %v, want failed auth followed by next auth", calls)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 1 || calls[0] != "bb-other-auth" {
		t.Fatalf("Execute() calls = %v, want only prepared healthy auth", calls)
	}
}

func TestManagerExecute_EgressDisabledAllCredentialsReturnsLastError(t *testing.T) {
	const model = "gpt-5.3-codex"
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "codex",
		executeErrors: map[string]error{
			"aa-bound-auth": &egress.Error{Code: "egress_disabled", Message: "first endpoint unavailable"},
			"bb-other-auth": &egress.Error{Code: "egress_disabled", Message: "second endpoint unavailable"},
		},
	}
	m.RegisterExecutor(executor)
	registerFallbackAuths(t, m, model)

	_, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	var runtimeErr *egress.Error
	if !errors.As(err, &runtimeErr) || runtimeErr.Message != "second endpoint unavailable" {
		t.Fatalf("Execute() error = %v, want last egress_disabled error", err)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 2 || calls[0] != "aa-bound-auth" || calls[1] != "bb-other-auth" {
		t.Fatalf("Execute() calls = %v, want both bound auths", calls)
	}
}

func TestManagerExecute_OtherEgressErrorsRemainTerminal(t *testing.T) {
	const model = "gpt-5.3-codex"
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "codex",
		executeErrors: map[string]error{
			"aa-bound-auth": egress.RuntimeError(egress.ErrEgressRequired),
		},
	}
	m.RegisterExecutor(executor)
	registerFallbackAuths(t, m, model)

	_, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	var runtimeErr *egress.Error
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "egress_required" {
		t.Fatalf("Execute() error = %v, want egress_required", err)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 1 || calls[0] != "aa-bound-auth" {
		t.Fatalf("Execute() calls = %v, want terminal egress error to stop fallback", calls)
	}
}

func TestManagerExecuteStream_EgressDisabledBeforeFirstChunkFallsBackToNextCredential(t *testing.T) {
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
		t.Fatal("ExecuteStream() closed before payload chunk")
	}
	if chunk.Err != nil {
		t.Fatalf("stream error = %v", chunk.Err)
	}
	if got := string(chunk.Payload); got != "bb-other-auth" {
		t.Fatalf("stream payload = %q, want next healthy auth", got)
	}
	if calls := executor.StreamCalls(); len(calls) != 2 || calls[0] != "aa-bound-auth" || calls[1] != "bb-other-auth" {
		t.Fatalf("ExecuteStream() calls = %v, want failed auth followed by next auth", calls)
	}
}

func TestManagerExecuteStream_EgressUnboundFallsBackForHTTPAndWebsocket(t *testing.T) {
	const model = "gpt-5.3-codex"
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "http", ctx: context.Background()},
		{name: "websocket", ctx: cliproxyexecutor.WithDownstreamWebsocket(context.Background())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			executor := &authFallbackExecutor{
				id: "codex",
				streamExecuteErrors: map[string]error{
					"aa-bound-auth": egress.RuntimeError(egress.ErrEgressUnbound),
				},
			}
			m.RegisterExecutor(executor)
			registerFallbackAuths(t, m, model)

			result, err := m.ExecuteStream(tc.ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}
			chunk, ok := <-result.Chunks
			if !ok || chunk.Err != nil {
				t.Fatalf("stream chunk = %#v, want healthy payload", chunk)
			}
			if got := string(chunk.Payload); got != "bb-other-auth" {
				t.Fatalf("stream payload = %q, want next healthy auth", got)
			}
			if calls := executor.StreamCalls(); len(calls) != 2 || calls[0] != "aa-bound-auth" || calls[1] != "bb-other-auth" {
				t.Fatalf("ExecuteStream() calls = %v, want failed auth followed by next auth", calls)
			}
		})
	}
}

func TestManagerExecuteStream_CredentialLocalEgressErrorReleasesPinnedWebsocketAuth(t *testing.T) {
	const model = "gpt-5.3-codex"
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "unbound", err: egress.ErrEgressUnbound},
		{name: "disabled", err: egress.ErrEndpointDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			executor := &authFallbackExecutor{
				id: "codex",
				streamExecuteErrors: map[string]error{
					"aa-bound-auth": egress.RuntimeError(tc.err),
				},
			}
			m.RegisterExecutor(executor)
			registerFallbackAuths(t, m, model)
			selected := make([]string, 0, 2)
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.PinnedAuthMetadataKey: "aa-bound-auth",
				cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
					selected = append(selected, authID)
				},
			}}

			result, err := m.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}
			chunk, ok := <-result.Chunks
			if !ok || chunk.Err != nil || string(chunk.Payload) != "bb-other-auth" {
				t.Fatalf("stream chunk = %#v, want healthy fallback payload", chunk)
			}
			if calls := executor.StreamCalls(); len(calls) != 2 || calls[0] != "aa-bound-auth" || calls[1] != "bb-other-auth" {
				t.Fatalf("ExecuteStream() calls = %v, want pinned auth followed by fallback auth", calls)
			}
			if len(selected) != 2 || selected[0] != "aa-bound-auth" || selected[1] != "bb-other-auth" {
				t.Fatalf("selected auth callbacks = %v, want both attempts", selected)
			}
		})
	}
}

func TestManagerExecuteStream_ResponseHeaderTimeoutFallsBackToNextCredential(t *testing.T) {
	const model = "gpt-5.3-codex"
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "codex",
		streamExecuteErrors: map[string]error{
			"aa-bound-auth": &Error{HTTPStatus: http.StatusGatewayTimeout, Message: "upstream response header timeout"},
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
		t.Fatal("ExecuteStream() closed before payload chunk")
	}
	if chunk.Err != nil {
		t.Fatalf("stream error = %v", chunk.Err)
	}
	if got := string(chunk.Payload); got != "bb-other-auth" {
		t.Fatalf("stream payload = %q, want next healthy auth", got)
	}
	if calls := executor.StreamCalls(); len(calls) != 2 || calls[0] != "aa-bound-auth" || calls[1] != "bb-other-auth" {
		t.Fatalf("ExecuteStream() calls = %v, want timed-out auth followed by next auth", calls)
	}

	timedOutAuth, ok := m.GetByID("aa-bound-auth")
	if !ok || timedOutAuth == nil {
		t.Fatal("timed-out auth not found")
	}
	state := timedOutAuth.ModelStates[model]
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("timed-out auth state = %#v, want transient cooldown", state)
	}
}

func TestManagerExecuteStream_EgressDisabledDuringPrepareFallsBackToNextCredential(t *testing.T) {
	const model = "gpt-5.3-codex"
	m := NewManager(nil, nil, nil)
	executor := &egressPrepareFallbackExecutor{
		authFallbackExecutor: &authFallbackExecutor{id: "codex"},
		prepareErrors: map[string]error{
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
		t.Fatal("ExecuteStream() closed before payload chunk")
	}
	if chunk.Err != nil {
		t.Fatalf("stream error = %v", chunk.Err)
	}
	if got := string(chunk.Payload); got != "bb-other-auth" {
		t.Fatalf("stream payload = %q, want next healthy auth", got)
	}
	if calls := executor.PrepareCalls(); len(calls) != 2 || calls[0] != "aa-bound-auth" || calls[1] != "bb-other-auth" {
		t.Fatalf("PrepareRequestAuth() calls = %v, want failed auth followed by next auth", calls)
	}
	if calls := executor.StreamCalls(); len(calls) != 1 || calls[0] != "bb-other-auth" {
		t.Fatalf("ExecuteStream() calls = %v, want only prepared healthy auth", calls)
	}
}

func TestManagerExecuteStream_EgressDisabledAllCredentialsReturnsLastError(t *testing.T) {
	const model = "gpt-5.3-codex"
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "codex",
		streamFirstErrors: map[string]error{
			"aa-bound-auth": &egress.Error{Code: "egress_disabled", Message: "first endpoint unavailable"},
			"bb-other-auth": &egress.Error{Code: "egress_disabled", Message: "second endpoint unavailable"},
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
	if !errors.As(chunk.Err, &runtimeErr) || runtimeErr.Message != "second endpoint unavailable" {
		t.Fatalf("stream error = %v, want last egress_disabled error", chunk.Err)
	}
	if calls := executor.StreamCalls(); len(calls) != 2 || calls[0] != "aa-bound-auth" || calls[1] != "bb-other-auth" {
		t.Fatalf("ExecuteStream() calls = %v, want both bound auths", calls)
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

type egressPrepareFallbackExecutor struct {
	*authFallbackExecutor
	prepareErrors map[string]error
	prepareCalls  []string
}

func (e *egressPrepareFallbackExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }

func (e *egressPrepareFallbackExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls = append(e.prepareCalls, auth.ID)
	if err := e.prepareErrors[auth.ID]; err != nil {
		return auth, err
	}
	return auth, nil
}

func (e *egressPrepareFallbackExecutor) PrepareCalls() []string {
	return append([]string(nil), e.prepareCalls...)
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
