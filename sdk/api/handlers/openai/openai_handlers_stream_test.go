package openai

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type blockingChatBootstrapExecutor struct {
	chunks         chan coreexecutor.StreamChunk
	executeStarted chan struct{}
	releaseExecute chan struct{}
	bootstrapErr   error
}

func (e *blockingChatBootstrapExecutor) Identifier() string {
	return "chat-bootstrap-blocker"
}

func (e *blockingChatBootstrapExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *blockingChatBootstrapExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	select {
	case e.executeStarted <- struct{}{}:
	default:
	}
	if e.bootstrapErr != nil {
		return nil, e.bootstrapErr
	}
	select {
	case <-e.releaseExecute:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &coreexecutor.StreamResult{
		Headers: http.Header{"Content-Type": {"text/event-stream"}},
		Chunks:  e.chunks,
	}, nil
}

func (e *blockingChatBootstrapExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *blockingChatBootstrapExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *blockingChatBootstrapExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestChatCompletionsStreamingWritesKeepAliveBeforeBootstrap(t *testing.T) {
	const model = "chat-bootstrap-model"
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk)
	executeStarted := make(chan struct{}, 1)
	releaseExecute := make(chan struct{})
	executor := &blockingChatBootstrapExecutor{
		chunks:         chunks,
		executeStarted: executeStarted,
		releaseExecute: releaseExecute,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "chat-bootstrap-auth",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1},
	}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)
	server := httptest.NewServer(router)

	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			close(releaseExecute)
			close(chunks)
		})
	}
	defer server.Close()
	defer finish()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	type responseResult struct {
		resp *http.Response
		err  error
	}
	responseCh := make(chan responseResult, 1)
	go func() {
		resp, errDo := server.Client().Do(req)
		responseCh <- responseResult{resp: resp, err: errDo}
	}()

	select {
	case <-executeStarted:
	case <-time.After(time.Second):
		t.Fatal("executor did not enter synchronous bootstrap")
	}

	var result responseResult
	select {
	case result = <-responseCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("SSE headers and heartbeat were not flushed during synchronous bootstrap")
	}
	if result.err != nil {
		t.Fatalf("post chat completions stream: %v", result.err)
	}
	defer result.resp.Body.Close()
	if result.resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", result.resp.StatusCode, http.StatusOK)
	}
	if got := result.resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}

	reader := bufio.NewReader(result.resp.Body)
	heartbeat, errRead := reader.ReadString('\n')
	if errRead != nil {
		t.Fatalf("read bootstrap heartbeat: %v", errRead)
	}
	if heartbeat != ": keep-alive\n" {
		t.Fatalf("bootstrap heartbeat = %q, want SSE comment heartbeat", heartbeat)
	}
	terminator, errRead := reader.ReadString('\n')
	if errRead != nil {
		t.Fatalf("read bootstrap heartbeat terminator: %v", errRead)
	}
	if terminator != "\n" {
		t.Fatalf("bootstrap heartbeat terminator = %q, want blank line", terminator)
	}

	finish()
}

func newChatBootstrapErrorServer(t *testing.T, keepAliveSeconds int, model string, executor *blockingChatBootstrapExecutor) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "chat-bootstrap-error-auth-" + model,
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: keepAliveSeconds},
	}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

func TestChatCompletionsStreamingWithoutKeepAlivePreservesBootstrapHTTPError(t *testing.T) {
	const model = "chat-bootstrap-http-error-model"
	executor := &blockingChatBootstrapExecutor{
		executeStarted: make(chan struct{}, 1),
		bootstrapErr:   &coreauth.Error{Code: "rate_limit", Message: "upstream rate limited", HTTPStatus: http.StatusTooManyRequests},
	}
	server := newChatBootstrapErrorServer(t, 0, model, executor)

	resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("post chat completions stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTooManyRequests)
	}
}

func TestChatCompletionsStreamingWithKeepAliveEmitsBootstrapErrorEvent(t *testing.T) {
	const model = "chat-bootstrap-sse-error-model"
	executor := &blockingChatBootstrapExecutor{
		executeStarted: make(chan struct{}, 1),
		bootstrapErr:   &coreauth.Error{Code: "rate_limit", Message: "upstream rate limited", HTTPStatus: http.StatusTooManyRequests},
	}
	server := newChatBootstrapErrorServer(t, 1, model, executor)

	resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("post chat completions stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read SSE error body: %v", err)
	}
	text := string(body)
	if !strings.HasPrefix(text, ": keep-alive\n\n") {
		t.Fatalf("SSE body does not start with bootstrap heartbeat: %q", text)
	}
	if !strings.Contains(text, "data: ") || !strings.Contains(text, "upstream rate limited") {
		t.Fatalf("SSE body does not contain bootstrap error event: %q", text)
	}
}
