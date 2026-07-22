package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestManagerKeepsPreviousResponseOnCreatingAuth(t *testing.T) {
	const model = "gpt-5.6-sol"

	manager := NewManager(nil, nil, nil)
	executor := &responseAffinityTestExecutor{id: "codex"}
	manager.RegisterExecutor(executor)

	authA := &Auth{ID: "response-affinity-a", Provider: "codex", Status: StatusActive}
	authB := &Auth{ID: "response-affinity-b", Provider: "codex", Status: StatusActive}
	for _, auth := range []*Auth{authA, authB} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA.ID)
		registry.GetGlobalRegistry().UnregisterClient(authB.ID)
	})

	first, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"first"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    "openai-response",
		OriginalRequest: []byte(`{"model":"gpt-5.6-sol","input":"first"}`),
	})
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if got := string(first.Payload); got != `{"id":"response-affinity-a","object":"response"}` {
		t.Fatalf("first response = %s, want auth A response", got)
	}

	_, err = manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"gpt-5.6-sol","previous_response_id":"response-affinity-a","input":"second"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    "openai-response",
		OriginalRequest: []byte(`{"model":"gpt-5.6-sol","previous_response_id":"response-affinity-a","input":"second"}`),
	})
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}

	calls := executor.ExecuteCalls()
	if len(calls) != 2 || calls[0] != authA.ID || calls[1] != authA.ID {
		t.Fatalf("Execute() auth calls = %v, want [%s %s]", calls, authA.ID, authA.ID)
	}
}

func TestManagerKeepsStreamingPreviousResponseOnCreatingAuth(t *testing.T) {
	const model = "gpt-5.6-sol"

	manager := NewManager(nil, nil, nil)
	executor := &responseAffinityTestExecutor{id: "codex"}
	manager.RegisterExecutor(executor)

	authA := &Auth{ID: "response-affinity-stream-a", Provider: "codex", Status: StatusActive}
	authB := &Auth{ID: "response-affinity-stream-b", Provider: "codex", Status: StatusActive}
	for _, auth := range []*Auth{authA, authB} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA.ID)
		registry.GetGlobalRegistry().UnregisterClient(authB.ID)
	})

	firstRequest := []byte(`{"model":"gpt-5.6-sol","input":"first"}`)
	first, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   model,
		Payload: firstRequest,
	}, cliproxyexecutor.Options{
		SourceFormat:    "openai-response",
		OriginalRequest: firstRequest,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("first ExecuteStream() error = %v", err)
	}
	for chunk := range first.Chunks {
		if chunk.Err != nil {
			t.Fatalf("first stream chunk error = %v", chunk.Err)
		}
	}

	secondRequest := []byte(`{"model":"gpt-5.6-sol","previous_response_id":"response-affinity-stream-a","input":"second"}`)
	second, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   model,
		Payload: secondRequest,
	}, cliproxyexecutor.Options{
		SourceFormat:    "openai-response",
		OriginalRequest: secondRequest,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("second ExecuteStream() error = %v", err)
	}
	for chunk := range second.Chunks {
		if chunk.Err != nil {
			t.Fatalf("second stream chunk error = %v", chunk.Err)
		}
	}

	calls := executor.StreamCalls()
	if len(calls) != 2 || calls[0] != authA.ID || calls[1] != authA.ID {
		t.Fatalf("ExecuteStream() auth calls = %v, want [%s %s]", calls, authA.ID, authA.ID)
	}
}

func TestManagerDoesNotFailOverPreviousResponseWhenCreatingAuthIsUnavailable(t *testing.T) {
	const model = "gpt-5.6-sol"

	manager := NewManager(nil, nil, nil)
	executor := &responseAffinityTestExecutor{id: "codex", previousResponseErrors: make(map[string]error)}
	manager.RegisterExecutor(executor)

	authA := &Auth{ID: "response-affinity-error-a", Provider: "codex", Status: StatusActive}
	authB := &Auth{ID: "response-affinity-error-b", Provider: "codex", Status: StatusActive}
	for _, auth := range []*Auth{authA, authB} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA.ID)
		registry.GetGlobalRegistry().UnregisterClient(authB.ID)
	})

	firstRequest := []byte(`{"model":"gpt-5.6-sol","input":"first"}`)
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model: model, Payload: firstRequest,
	}, cliproxyexecutor.Options{SourceFormat: "openai-response", OriginalRequest: firstRequest}); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	executor.SetPreviousResponseError(authA.ID, egress.RuntimeError(egress.ErrEgressUnbound))

	secondRequest := []byte(`{"model":"gpt-5.6-sol","previous_response_id":"response-affinity-a","input":"second"}`)
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model: model, Payload: secondRequest,
	}, cliproxyexecutor.Options{SourceFormat: "openai-response", OriginalRequest: secondRequest})
	if err == nil {
		t.Fatal("second Execute() error = nil, want original auth failure")
	}
	if got := statusCodeFromError(err); got != http.StatusBadRequest {
		t.Fatalf("second Execute() status = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	if !strings.Contains(err.Error(), "previous_response_not_found") {
		t.Fatalf("second Execute() error = %v, want previous_response_not_found replay signal", err)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 2 || calls[0] != authA.ID || calls[1] != authA.ID {
		t.Fatalf("Execute() auth calls = %v, want original auth only", calls)
	}
}

func TestManagerRequestsFullReplayWhenCreatingAuthWasRemoved(t *testing.T) {
	const model = "gpt-5.6-sol"

	manager := NewManager(nil, nil, nil)
	executor := &responseAffinityTestExecutor{id: "codex"}
	manager.RegisterExecutor(executor)
	authA := &Auth{ID: "response-affinity-removed-a", Provider: "codex", Status: StatusActive}
	authB := &Auth{ID: "response-affinity-removed-b", Provider: "codex", Status: StatusActive}
	for _, auth := range []*Auth{authA, authB} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA.ID)
		registry.GetGlobalRegistry().UnregisterClient(authB.ID)
	})

	firstRequest := []byte(`{"model":"gpt-5.6-sol","input":"first"}`)
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model: model, Payload: firstRequest,
	}, cliproxyexecutor.Options{SourceFormat: "openai-response", OriginalRequest: firstRequest}); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	manager.Remove(context.Background(), authA.ID)

	secondRequest := []byte(`{"model":"gpt-5.6-sol","previous_response_id":"response-affinity-a","input":"second"}`)
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model: model, Payload: secondRequest,
	}, cliproxyexecutor.Options{SourceFormat: "openai-response", OriginalRequest: secondRequest})
	if err == nil || statusCodeFromError(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "previous_response_not_found") {
		t.Fatalf("second Execute() error = %v, want previous_response_not_found replay signal", err)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 1 || calls[0] != authA.ID {
		t.Fatalf("Execute() auth calls = %v, want removed auth only on first turn", calls)
	}
}

func TestPickNextViaHomeReusesPreviousResponseCreatingAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(&responseAffinityTestExecutor{id: "codex"})

	auth := &Auth{ID: "home-response-affinity-auth", Provider: "codex", Status: StatusActive}
	firstRequest := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"input":"first"}`)}
	firstOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"Session_id": {"stable-session"}},
		OriginalRequest: firstRequest.Payload,
	}
	manager.responseAffinity.Set("home-response-affinity-id", responseAffinityBinding{
		auth:     auth,
		authID:   auth.ID,
		provider: "codex",
		scope:    responseAffinityScope(firstOpts),
	})

	secondRequest := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"previous_response_id":"home-response-affinity-id","input":"second"}`)}
	secondOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"Session_id": {"stable-session"}},
		OriginalRequest: secondRequest.Payload,
	}
	secondOpts = manager.applyPreviousResponseAffinity(secondRequest, secondOpts)

	previousDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return homeAuthTransportErrorDispatcher{err: errors.New("home dispatch must not run for a bound response")}
	}
	t.Cleanup(func() { currentHomeDispatcher = previousDispatcher })

	got, executor, provider, errPick := manager.pickNextViaHome(context.Background(), secondRequest.Model, secondOpts, nil)
	if errPick != nil {
		t.Fatalf("pickNextViaHome() error = %v", errPick)
	}
	if got == nil || got.ID != auth.ID {
		t.Fatalf("pickNextViaHome() auth = %#v, want %s", got, auth.ID)
	}
	if executor == nil || provider != "codex" {
		t.Fatalf("pickNextViaHome() executor/provider = %#v/%q, want codex", executor, provider)
	}
}

func TestResponseIDsFromPayload(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		payload string
		want    []string
	}{
		{name: "response object", payload: `{"id":"resp-root","object":"response"}`, want: []string{"resp-root"}},
		{name: "stream event", payload: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-stream\"}}\n\n", want: []string{"resp-stream"}},
		{name: "output item is not a response", payload: `{"id":"item-1","object":"response.output_item"}`},
		{name: "invalid payload", payload: `not-json`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := responseIDsFromPayload([]byte(testCase.payload)); !slices.Equal(got, testCase.want) {
				t.Fatalf("response IDs = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestResponseAffinityCacheEvictsBoundHomeAuth(t *testing.T) {
	cache := newResponseAffinityCache(time.Hour, 1)
	cache.Set("resp-a", responseAffinityBinding{auth: &Auth{ID: "auth-a"}, authID: "auth-a"})
	cache.Set("resp-b", responseAffinityBinding{auth: &Auth{ID: "auth-b"}, authID: "auth-b"})

	if _, ok := cache.GetAndRefresh("resp-a"); ok {
		t.Fatal("evicted response affinity is still present")
	}
	if _, ok := cache.homeAuths["auth-a"]; ok {
		t.Fatal("evicted response affinity retained its Home auth clone")
	}
	if binding, ok := cache.GetAndRefresh("resp-b"); !ok || binding.authID != "auth-b" {
		t.Fatalf("newest response affinity = %#v, %v; want auth-b", binding, ok)
	}
}

type responseAffinityTestExecutor struct {
	id string

	mu                     sync.Mutex
	executeCalls           []string
	streamCalls            []string
	previousResponseErrors map[string]error
}

func (e *responseAffinityTestExecutor) Identifier() string { return e.id }

func (e *responseAffinityTestExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	err := e.previousResponseErrors[auth.ID]
	e.mu.Unlock()
	if strings.TrimSpace(gjson.GetBytes(req.Payload, "previous_response_id").String()) != "" && err != nil {
		return cliproxyexecutor.Response{}, err
	}
	payload := `{"id":"response-affinity-a","object":"response"}`
	if auth.ID != "" && len(auth.ID) > 0 && auth.ID[len(auth.ID)-1] == 'b' {
		payload = `{"id":"response-affinity-b","object":"response"}`
	}
	return cliproxyexecutor.Response{Payload: []byte(payload)}, nil
}

func (e *responseAffinityTestExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	e.mu.Unlock()
	responseID := "response-affinity-stream-a"
	if auth.ID != "" && auth.ID[len(auth.ID)-1] == 'b' {
		responseID = "response-affinity-stream-b"
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed","response":{"id":"` + responseID + `"}}` + "\n\n")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *responseAffinityTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *responseAffinityTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", Message: "count tokens not used"}
}

func (e *responseAffinityTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "not_implemented", Message: "http request not used"}
}

func (e *responseAffinityTestExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.executeCalls...)
}

func (e *responseAffinityTestExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.streamCalls...)
}

func (e *responseAffinityTestExecutor) SetPreviousResponseError(authID string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.previousResponseErrors == nil {
		e.previousResponseErrors = make(map[string]error)
	}
	e.previousResponseErrors[authID] = err
}
