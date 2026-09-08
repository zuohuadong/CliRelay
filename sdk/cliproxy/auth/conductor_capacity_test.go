package auth

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func registerCapacityRetryAuths(t *testing.T, m *Manager, n int) []string {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("capacity-retry-auth-%d", i+1)
		auth := &Auth{
			ID:         id,
			Provider:   "codex",
			Status:     StatusActive,
			Attributes: map[string]string{"priority": fmt.Sprintf("%d", 100-i)},
			Metadata:   map[string]any{"access_token": "test-token"},
		}
		reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-5.6-terra"}})
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			reg.UnregisterClient(id)
		}
	})
	return ids
}

func immediateOverloadStatusError() customStatusError {
	delay := time.Duration(0)
	err := overloadStatusError()
	err.retryAfter = &delay
	return err
}

func enableCapacitySameAccountRetries(m *Manager, retries int) {
	m.SetConfigSnapshot(&internalconfig.Config{
		Codex: internalconfig.CodexConfig{CapacitySameAccountRetries: &retries},
	})
}

func TestExecuteStream_CapacityRetriesSameAccountBeforeCredentialFailover(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 2)
	enableCapacitySameAccountRetries(m, 3)
	ids := registerCapacityRetryAuths(t, m, 2)

	var mu sync.Mutex
	var order []string
	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			mu.Lock()
			order = append(order, auth.ID)
			mu.Unlock()
			if auth.ID == ids[0] {
				return nil, immediateOverloadStatusError()
			}
			return successStreamResult(), nil
		},
	})

	result, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{ids[0], ids[0], ids[0], ids[0], ids[1]}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("attempt order = %v, want %v", order, want)
	}
}

func TestExecuteStream_BootstrapCapacitySuccessKeepsSameAccount(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 2)
	enableCapacitySameAccountRetries(m, 3)
	ids := registerCapacityRetryAuths(t, m, 2)

	var mu sync.Mutex
	var order []string
	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			mu.Lock()
			order = append(order, auth.ID)
			attempt := len(order)
			mu.Unlock()
			if attempt == 1 {
				return streamErrorResult(http.Header{"X-Upstream": []string{"capacity"}}, immediateOverloadStatusError()), nil
			}
			return successStreamResult(), nil
		},
	})

	result, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{ids[0], ids[0]}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("attempt order = %v, want %v", order, want)
	}
}

func TestExecute_CapacityRetriesSameAccountBeforeCredentialFailover(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 2)
	enableCapacitySameAccountRetries(m, 3)
	ids := registerCapacityRetryAuths(t, m, 2)

	var mu sync.Mutex
	var order []string
	m.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			mu.Lock()
			order = append(order, auth.ID)
			mu.Unlock()
			if auth.ID == ids[0] {
				return cliproxyexecutor.Response{}, immediateOverloadStatusError()
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
		},
	})

	if _, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{ids[0], ids[0], ids[0], ids[0], ids[1]}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("attempt order = %v, want %v", order, want)
	}
}

func TestIsUpstreamCapacityOverloadErrorRejectsRequestAndAuthErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden} {
		err := customStatusError{code: status, msg: http.StatusText(status)}
		if isUpstreamCapacityOverloadError(err) {
			t.Fatalf("status %d must not trigger same-account capacity retry", status)
		}
	}
}

func TestIsUpstreamCapacityOverloadErrorRecognizesCapacitySignals(t *testing.T) {
	for _, err := range []error{
		customStatusError{code: http.StatusTooManyRequests, msg: `{"error":{"message":"Selected model is at capacity. Please try a different model."}}`},
		customStatusError{code: http.StatusServiceUnavailable, msg: `{"error":{"type":"service_unavailable_error"}}`},
		customStatusError{code: http.StatusTooManyRequests, msg: `{"error":{"code":"slow_down"}}`},
		overloadStatusError(),
	} {
		if !isUpstreamCapacityOverloadError(err) {
			t.Fatalf("expected capacity overload classification for %v", err)
		}
	}
}

func TestCapacitySameAccountRetryDelay(t *testing.T) {
	for attempt, want := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second} {
		if got := capacitySameAccountRetryDelay(overloadStatusError(), attempt); got != want {
			t.Fatalf("attempt %d delay = %v, want %v", attempt, got, want)
		}
	}

	retryAfter := 3 * time.Second
	err := overloadStatusError()
	err.retryAfter = &retryAfter
	if got := capacitySameAccountRetryDelay(err, 0); got != retryAfter {
		t.Fatalf("Retry-After delay = %v, want %v", got, retryAfter)
	}
}

func TestEffectiveCapacitySameAccountRetries(t *testing.T) {
	m := NewManager(nil, nil, nil)
	oauth := &Auth{Provider: "codex", Metadata: map[string]any{"access_token": "test-token"}}

	m.SetConfigSnapshot(&internalconfig.Config{Codex: internalconfig.CodexConfig{StreamBootstrapBuffering: true}})
	if got := m.effectiveCapacitySameAccountRetries(oauth); got != 3 {
		t.Fatalf("buffering default retries = %d, want 3", got)
	}

	global := 2
	m.SetConfigSnapshot(&internalconfig.Config{CapacitySameAccountRetries: &global})
	if got := m.effectiveCapacitySameAccountRetries(oauth); got != 2 {
		t.Fatalf("global retries = %d, want 2", got)
	}

	disabled := 0
	m.SetConfigSnapshot(&internalconfig.Config{
		CapacitySameAccountRetries: &global,
		Codex:                      internalconfig.CodexConfig{CapacitySameAccountRetries: &disabled},
	})
	if got := m.effectiveCapacitySameAccountRetries(oauth); got != 0 {
		t.Fatalf("codex override retries = %d, want 0", got)
	}
}
