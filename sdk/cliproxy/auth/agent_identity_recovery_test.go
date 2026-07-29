package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type invalidAgentTaskTestError struct{}

func (invalidAgentTaskTestError) Error() string     { return "invalid task" }
func (invalidAgentTaskTestError) StatusCode() int   { return 401 }
func (invalidAgentTaskTestError) ErrorCode() string { return "invalid_task_id" }
func (invalidAgentTaskTestError) RequestAuthScheme() string {
	return "AgentAssertion"
}

type genericAgentAssertionUnauthorizedError struct{}

func (genericAgentAssertionUnauthorizedError) Error() string   { return "unauthorized assertion" }
func (genericAgentAssertionUnauthorizedError) StatusCode() int { return 401 }
func (genericAgentAssertionUnauthorizedError) RequestAuthScheme() string {
	return "AgentAssertion"
}

type agentTaskRenewalTestExecutor struct {
	schedulerProviderTestExecutor
	renewals atomic.Int32
}

type agentIdentityStreamRecoveryExecutor struct {
	schedulerProviderTestExecutor
	streamCalls              atomic.Int32
	renewals                 atomic.Int32
	mu                       sync.Mutex
	taskIDs                  []string
	payloadBeforeInvalidTask bool
}

func (executor *agentIdentityStreamRecoveryExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	taskID := authMetadataString(auth, "task_id")
	executor.streamCalls.Add(1)
	executor.mu.Lock()
	executor.taskIDs = append(executor.taskIDs, taskID)
	executor.mu.Unlock()
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	if taskID == "task-old" {
		if executor.payloadBeforeInvalidTask {
			chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created"}`)}
		}
		chunks <- cliproxyexecutor.StreamChunk{Err: invalidAgentTaskTestError{}}
	} else {
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created"}`)}
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (executor *agentIdentityStreamRecoveryExecutor) RenewAgentIdentityTask(_ context.Context, auth *Auth) (*Auth, error) {
	executor.renewals.Add(1)
	updated := auth.Clone()
	updated.Metadata["task_id"] = "task-new"
	return updated, nil
}

func (executor *agentIdentityStreamRecoveryExecutor) observedTaskIDs() []string {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]string(nil), executor.taskIDs...)
}

func (executor *agentTaskRenewalTestExecutor) RenewAgentIdentityTask(_ context.Context, auth *Auth) (*Auth, error) {
	executor.renewals.Add(1)
	time.Sleep(10 * time.Millisecond)
	updated := auth.Clone()
	updated.Metadata["task_id"] = "task-new"
	return updated, nil
}

func TestAgentIdentityTaskRenewalIsSingleFlightAndDurable(t *testing.T) {
	store := &metadataMergeStore{}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	executor := &agentTaskRenewalTestExecutor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"}}
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID: "agent-auth", Index: "agent-index", Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{"task_id": "task-old", "agent_identity_state": "ready"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	const callers = 8
	var waitGroup sync.WaitGroup
	errorsSeen := make(chan error, callers)
	for range callers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			renewed, retried, err := manager.tryRefreshAfterUnauthorized(context.Background(), auth.Clone(), invalidAgentTaskTestError{}, false)
			if err == nil && (!retried || authMetadataString(renewed, "task_id") != "task-new") {
				err = errors.New("renewal did not return the persisted task")
			}
			errorsSeen <- err
		}()
	}
	waitGroup.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent renewal error = %v", err)
		}
	}
	if got := executor.renewals.Load(); got != 1 {
		t.Fatalf("task registrations = %d, want 1", got)
	}
	persisted, ok := manager.GetByID(auth.ID)
	if !ok || authMetadataString(persisted, "task_id") != "task-new" {
		t.Fatalf("persisted task = %q, found = %t", authMetadataString(persisted, "task_id"), ok)
	}
}

func TestAgentIdentityTaskRenewalDoesNotReplayTwice(t *testing.T) {
	manager := NewManager(&metadataMergeStore{}, &RoundRobinSelector{}, nil)
	executor := &agentTaskRenewalTestExecutor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"}}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "agent-auth", Index: "agent-index", Provider: "codex", Metadata: map[string]any{"task_id": "task-old"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	_, retried, err := manager.tryRefreshAfterUnauthorized(context.Background(), auth, invalidAgentTaskTestError{}, true)
	if err == nil || retried {
		t.Fatalf("second invalid_task_id = retried:%t error:%v", retried, err)
	}
	if got := executor.renewals.Load(); got != 0 {
		t.Fatalf("task registrations = %d, want 0", got)
	}
}

func TestManagerDownstreamWebsocketRenewsInvalidAgentTaskAndReplaysOnce(t *testing.T) {
	const model = "agent-identity-websocket-recovery"
	store := &metadataMergeStore{}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	executor := &agentIdentityStreamRecoveryExecutor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"}}
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID: "agent-auth", Index: "agent-index", Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{"task_id": "task-old", "agent_identity_state": "ready"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	stream, err := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	chunk, ok := <-stream.Chunks
	if !ok || chunk.Err != nil || string(chunk.Payload) != `{"type":"response.created"}` {
		t.Fatalf("replayed stream chunk = payload:%s error:%v open:%t", chunk.Payload, chunk.Err, ok)
	}
	if got := executor.streamCalls.Load(); got != 2 {
		t.Fatalf("ExecuteStream calls = %d, want 2", got)
	}
	if got := executor.renewals.Load(); got != 1 {
		t.Fatalf("task renewals = %d, want 1", got)
	}
	if got := executor.observedTaskIDs(); len(got) != 2 || got[0] != "task-old" || got[1] != "task-new" {
		t.Fatalf("stream task IDs = %v, want [task-old task-new]", got)
	}
	persisted, ok := manager.GetByID(auth.ID)
	if !ok || authMetadataString(persisted, "task_id") != "task-new" {
		t.Fatalf("persisted task = %q, found = %t", authMetadataString(persisted, "task_id"), ok)
	}
}

func TestManagerDoesNotReplayInvalidAgentTaskAfterVisiblePayload(t *testing.T) {
	const model = "agent-identity-visible-payload"
	manager := NewManager(&metadataMergeStore{}, &RoundRobinSelector{}, nil)
	executor := &agentIdentityStreamRecoveryExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
		payloadBeforeInvalidTask:      true,
	}
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID: "agent-auth", Index: "agent-index", Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{"task_id": "task-old", "agent_identity_state": "ready"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	stream, err := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	first := <-stream.Chunks
	second := <-stream.Chunks
	if len(first.Payload) == 0 || second.Err == nil {
		t.Fatalf("stream chunks = first payload:%s second error:%v", first.Payload, second.Err)
	}
	if executor.streamCalls.Load() != 1 || executor.renewals.Load() != 0 {
		t.Fatalf("visible stream replayed: calls=%d renewals=%d", executor.streamCalls.Load(), executor.renewals.Load())
	}
}

func TestManagedAgentIdentityUnauthorizedDoesNotRefreshOAuth(t *testing.T) {
	manager := NewManager(&metadataMergeStore{}, &RoundRobinSelector{}, nil)
	auth := &Auth{
		ID: "managed-agent", Provider: "codex",
		Metadata: map[string]any{
			"access_token": "access", "refresh_token": "refresh",
			"agent_identity_state": "ready",
		},
	}
	_, retried, err := manager.tryRefreshAfterUnauthorized(context.Background(), auth, genericAgentAssertionUnauthorizedError{}, false)
	if err == nil || retried {
		t.Fatalf("AgentAssertion 401 = retried:%t error:%v", retried, err)
	}
}

func TestManagedAgentIdentityBearerUnauthorizedRefreshesOAuth(t *testing.T) {
	manager := NewManager(&metadataMergeStore{}, &RoundRobinSelector{}, nil)
	executor := &unauthorizedRefreshExecutor{id: "codex", refreshTokens: map[string]string{"managed-agent": "fresh-access"}}
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID: "managed-agent", Index: "managed-agent-index", Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{
			"access_token": "stale-access", "refresh_token": "refresh",
			"agent_identity_state": "ready",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	refreshed, retried, err := manager.tryRefreshAfterUnauthorized(context.Background(), auth, &Error{HTTPStatus: 401, Message: "bearer unauthorized"}, false)
	if err != nil || !retried {
		t.Fatalf("Bearer 401 = retried:%t error:%v", retried, err)
	}
	if got := authAccessToken(refreshed); got != "fresh-access" {
		t.Fatalf("access_token = %q, want fresh-access", got)
	}
	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("Refresh calls = %d, want 1", got)
	}
}

func TestInvalidTaskFromBearerDoesNotRenewAgentIdentity(t *testing.T) {
	manager := NewManager(&metadataMergeStore{}, &RoundRobinSelector{}, nil)
	executor := &agentTaskRenewalTestExecutor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"}}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "oauth-auth", Provider: "codex", Metadata: map[string]any{"refresh_token": "refresh"}}
	_, retried, err := manager.tryRefreshAfterUnauthorized(context.Background(), auth, bearerInvalidTaskTestError{}, false)
	if err != nil || retried {
		t.Fatalf("Bearer invalid_task_id = retried:%t error:%v", retried, err)
	}
	if got := executor.renewals.Load(); got != 0 {
		t.Fatalf("task registrations = %d, want 0", got)
	}
}

func TestManagedAgentIdentityProbesDownstreamWebsocketBootstrap(t *testing.T) {
	auth := &Auth{Provider: "codex", Metadata: map[string]any{"agent_identity_state": "ready"}}
	if !authMayUseAgentAssertion(auth) {
		t.Fatal("managed Agent Identity must probe the first upstream websocket frame")
	}
}

type bearerInvalidTaskTestError struct{}

func (bearerInvalidTaskTestError) Error() string             { return "invalid task" }
func (bearerInvalidTaskTestError) StatusCode() int           { return 401 }
func (bearerInvalidTaskTestError) ErrorCode() string         { return "invalid_task_id" }
func (bearerInvalidTaskTestError) RequestAuthScheme() string { return "Bearer" }
