package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalegress "github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	requestlogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type websocketCaptureExecutor struct {
	streamCalls int
	payloads    [][]byte
}

type websocketBlockingExecutor struct {
	startedOnce  sync.Once
	canceledOnce sync.Once
	started      chan struct{}
	canceled     chan struct{}
	release      <-chan struct{}
	payload      []byte
}

type websocketCompactionCaptureExecutor struct {
	mu             sync.Mutex
	streamPayloads [][]byte
	compactPayload []byte
}

type orderedWebsocketSelector struct {
	mu     sync.Mutex
	order  []string
	cursor int
}

func (s *orderedWebsocketSelector) Pick(_ context.Context, _ string, _ string, _ coreexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(auths) == 0 {
		return nil, errors.New("no auth available")
	}
	for len(s.order) > 0 && s.cursor < len(s.order) {
		authID := strings.TrimSpace(s.order[s.cursor])
		s.cursor++
		for _, auth := range auths {
			if auth != nil && auth.ID == authID {
				return auth, nil
			}
		}
	}
	for _, auth := range auths {
		if auth != nil {
			return auth, nil
		}
	}
	return nil, errors.New("no auth available")
}

type websocketAuthCaptureExecutor struct {
	mu      sync.Mutex
	authIDs []string
}

type websocketPinnedFailoverExecutor struct {
	mu               sync.Mutex
	authIDs          []string
	calls            map[string]int
	payloads         map[string][][]byte
	secondAuthAError error
}

type websocketPreviousResponseReplayExecutor struct {
	mu           sync.Mutex
	calls        int
	payloads     [][]byte
	provider     string
	errorMessage string
	// failOnCall 让 executor 在指定轮次（从 1 开始）返回 errorMessage，默认 0 表示在第 2 轮失败。
	failOnCall  int
	firstOutput string
	// outputByCall 可按轮次覆盖默认 output 载荷，键为轮次（从 1 开始）。
	outputByCall map[int]string
}

type websocketZeroOutputEOFReplayExecutor struct {
	mu                         sync.Mutex
	calls                      int
	failCalls                  int
	firstCallLifecyclePayloads bool
	failPayload                []byte
	payloads                   [][]byte
}

type websocketTransientTransportFailoverExecutor struct {
	mu      sync.Mutex
	authIDs []string
}

func (e *websocketTransientTransportFailoverExecutor) Identifier() string { return "test-provider" }

func (e *websocketTransientTransportFailoverExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketTransientTransportFailoverExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.authIDs = append(e.authIDs, authID)
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	if authID == "auth-a" {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"error","error":{"message":"stream disconnected before completion: Transport error: network error: error decoding response body"}}`)}
	} else {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-auth-b","output":[{"type":"message","id":"out-auth-b"}]}}`)}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketTransientTransportFailoverExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketTransientTransportFailoverExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketTransientTransportFailoverExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketTransientTransportFailoverExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

type websocketBootstrapFallbackExecutor struct {
	mu       sync.Mutex
	authIDs  []string
	payloads map[string][][]byte
}

type websocketDirectCaptureExecutor struct {
	mu       sync.Mutex
	provider string
	authIDs  []string
	payloads [][]byte
	done     chan struct{}
	doneOnce sync.Once
}

type websocketPinnedFailoverStatusError struct {
	status int
	msg    string
}

func (e websocketPinnedFailoverStatusError) Error() string { return e.msg }

func (e websocketPinnedFailoverStatusError) StatusCode() int { return e.status }

func intPointer(v int) *int { return &v }

func xunfeiBusyWebsocketErrorPayload() []byte {
	return []byte(`{"type":"error","error":{"code":10012,"message":"Xunfei request failed with Sid: cht000d448b@dx19f3d101493ba5e452 code: 10012, msg: EngineInternalError:1105|{\"Code\":1105,\"Message\":\"The system is busy, please try again later.\"}"}}`)
}

func (e *websocketBootstrapFallbackExecutor) Identifier() string { return "test-provider" }

func (e *websocketBootstrapFallbackExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketBootstrapFallbackExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}

	e.mu.Lock()
	if e.payloads == nil {
		e.payloads = make(map[string][][]byte)
	}
	e.authIDs = append(e.authIDs, authID)
	e.payloads[authID] = append(e.payloads[authID], bytes.Clone(req.Payload))
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	if authID == "auth-ws" {
		chunks <- coreexecutor.StreamChunk{Err: websocketPinnedFailoverStatusError{
			status: http.StatusServiceUnavailable,
			msg:    `{"error":{"message":"websocket bootstrap failed","type":"server_error","code":"ws_failed"}}`,
		}}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}

	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-http","output":[{"type":"message","id":"out-http"}]}}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketBootstrapFallbackExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketBootstrapFallbackExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketBootstrapFallbackExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketBootstrapFallbackExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *websocketBootstrapFallbackExecutor) Payloads(authID string) [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	src := e.payloads[authID]
	out := make([][]byte, len(src))
	for i := range src {
		out[i] = bytes.Clone(src[i])
	}
	return out
}

func (e *websocketDirectCaptureExecutor) Identifier() string {
	if e != nil && strings.TrimSpace(e.provider) != "" {
		return strings.TrimSpace(e.provider)
	}
	return "codex"
}

func (e *websocketDirectCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketDirectCaptureExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.authIDs = append(e.authIDs, authID)
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	count := len(e.payloads)
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	responseID := fmt.Sprintf("resp-%d", count)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"output":[{"type":"message","id":"out-%d"}]}}`, responseID, count))}
	close(chunks)
	if count >= 2 && e.done != nil {
		e.doneOnce.Do(func() { close(e.done) })
	}
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketDirectCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketDirectCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketDirectCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketDirectCaptureExecutor) Payloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	for i := range e.payloads {
		out[i] = bytes.Clone(e.payloads[i])
	}
	return out
}

func (e *websocketDirectCaptureExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

type websocketUpstreamDisconnectExecutor struct {
	mu         sync.Mutex
	subscribed chan string
	sessions   map[string]chan error
}

func (e *websocketUpstreamDisconnectExecutor) Identifier() string { return "codex" }

func (e *websocketUpstreamDisconnectExecutor) UpstreamDisconnectChan(sessionID string) <-chan error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	e.mu.Lock()
	if e.sessions == nil {
		e.sessions = make(map[string]chan error)
	}
	ch, ok := e.sessions[sessionID]
	if !ok {
		ch = make(chan error, 1)
		e.sessions[sessionID] = ch
	}
	subscribed := e.subscribed
	e.mu.Unlock()

	if subscribed != nil {
		select {
		case subscribed <- sessionID:
		default:
		}
	}
	return ch
}

func (e *websocketUpstreamDisconnectExecutor) TriggerDisconnect(sessionID string, err error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	e.mu.Lock()
	ch := e.sessions[sessionID]
	delete(e.sessions, sessionID)
	e.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- err:
	default:
	}
	close(ch)
}

func (e *websocketUpstreamDisconnectExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketUpstreamDisconnectExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketUpstreamDisconnectExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketUpstreamDisconnectExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketUpstreamDisconnectExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) Identifier() string { return "test-provider" }

func (e *websocketAuthCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	if auth != nil {
		e.authIDs = append(e.authIDs, auth.ID)
	}
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-upstream","output":[{"type":"message","id":"out-1"}]}}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketAuthCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketAuthCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *websocketPinnedFailoverExecutor) Identifier() string { return "test-provider" }

func (e *websocketPinnedFailoverExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPinnedFailoverExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}

	e.mu.Lock()
	if e.calls == nil {
		e.calls = make(map[string]int)
	}
	if e.payloads == nil {
		e.payloads = make(map[string][][]byte)
	}
	e.authIDs = append(e.authIDs, authID)
	e.calls[authID]++
	call := e.calls[authID]
	e.payloads[authID] = append(e.payloads[authID], bytes.Clone(req.Payload))
	e.mu.Unlock()

	if authID == "auth-a" && (call == 2 || e.secondAuthAError != nil && call > 1) {
		if e.secondAuthAError != nil {
			return nil, e.secondAuthAError
		}
		chunks := make(chan coreexecutor.StreamChunk, 1)
		chunks <- coreexecutor.StreamChunk{Err: websocketPinnedFailoverStatusError{
			status: http.StatusTooManyRequests,
			msg:    `{"error":{"message":"quota exhausted","type":"rate_limit_error","code":"rate_limit_exceeded"}}`,
		}}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}

	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%s-%d","output":[{"type":"message","id":"out-%s-%d"}]}}`, authID, call, authID, call))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketPinnedFailoverExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketPinnedFailoverExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPinnedFailoverExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketPinnedFailoverExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *websocketPinnedFailoverExecutor) Payloads(authID string) [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	src := e.payloads[authID]
	out := make([][]byte, len(src))
	for i := range src {
		out[i] = bytes.Clone(src[i])
	}
	return out
}

func (e *websocketPreviousResponseReplayExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "test-provider"
}

func (e *websocketPreviousResponseReplayExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPreviousResponseReplayExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	failOnCall := e.failOnCall
	if failOnCall == 0 {
		failOnCall = 2
	}
	if call == failOnCall {
		errorMessage := e.errorMessage
		if errorMessage == "" {
			errorMessage = `{"error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"Previous response with id 'resp_missing' not found.","param":"previous_response_id"}}`
		}
		chunks <- coreexecutor.StreamChunk{Err: websocketPinnedFailoverStatusError{
			status: http.StatusBadRequest,
			msg:    errorMessage,
		}}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}

	output := fmt.Sprintf(`[{"type":"message","id":"out-%d"}]`, call)
	if call == 1 && e.firstOutput != "" {
		output = e.firstOutput
	}
	if e.outputByCall != nil {
		if override, ok := e.outputByCall[call]; ok && override != "" {
			output = override
		}
	}
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_replay_%d","output":%s}}`, call, output))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketPreviousResponseReplayExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketPreviousResponseReplayExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPreviousResponseReplayExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketPreviousResponseReplayExecutor) Payloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	for i := range e.payloads {
		out[i] = bytes.Clone(e.payloads[i])
	}
	return out
}

func (e *websocketZeroOutputEOFReplayExecutor) Identifier() string { return "test-provider" }

func (e *websocketZeroOutputEOFReplayExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketZeroOutputEOFReplayExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 4)
	failCalls := e.failCalls
	if failCalls == 0 {
		failCalls = 1
	}
	if call <= failCalls {
		if e.firstCallLifecyclePayloads {
			chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":"resp_eof_retry_%d","status":"in_progress","output":[]}}`, call))}
			chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.in_progress","response":{"id":"resp_eof_retry_%d","status":"in_progress","output":[]}}`, call))}
		}
		if len(e.failPayload) > 0 {
			chunks <- coreexecutor.StreamChunk{Payload: bytes.Clone(e.failPayload)}
		}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}

	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_eof_retry_%d","output":[{"type":"message","id":"out-%d"}]}}`, call, call))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketZeroOutputEOFReplayExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketZeroOutputEOFReplayExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketZeroOutputEOFReplayExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketZeroOutputEOFReplayExecutor) Payloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	for i := range e.payloads {
		out[i] = bytes.Clone(e.payloads[i])
	}
	return out
}

func (e *websocketCaptureExecutor) Identifier() string { return "test-provider" }

func (e *websocketCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.streamCalls++
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-upstream","output":[{"type":"message","id":"out-1"}]}}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketBlockingExecutor) Identifier() string { return "test-blocking" }

func (e *websocketBlockingExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketBlockingExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.startedOnce.Do(func() {
		if e.started != nil {
			close(e.started)
		}
	})
	select {
	case <-ctx.Done():
		e.canceledOnce.Do(func() {
			if e.canceled != nil {
				close(e.canceled)
			}
		})
		return nil, ctx.Err()
	case <-e.release:
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	if len(e.payload) > 0 {
		chunks <- coreexecutor.StreamChunk{Payload: bytes.Clone(e.payload)}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketBlockingExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketBlockingExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketBlockingExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketCompactionCaptureExecutor) Identifier() string { return "test-provider" }

func (e *websocketCompactionCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.compactPayload = bytes.Clone(req.Payload)
	e.mu.Unlock()
	if opts.Alt != "responses/compact" {
		return coreexecutor.Response{}, fmt.Errorf("unexpected non-compact execute alt: %q", opts.Alt)
	}
	return coreexecutor.Response{Payload: []byte(`{"id":"cmp-1","object":"response.compaction"}`)}, nil
}

func (e *websocketCompactionCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	callIndex := len(e.streamPayloads)
	e.streamPayloads = append(e.streamPayloads, bytes.Clone(req.Payload))
	e.mu.Unlock()

	var payload []byte
	switch callIndex {
	case 0:
		payload = []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"}]}}`)
	case 1:
		payload = []byte(`{"type":"response.completed","response":{"id":"resp-2","output":[{"type":"message","id":"assistant-1"}]}}`)
	default:
		payload = []byte(`{"type":"response.completed","response":{"id":"resp-3","output":[{"type":"message","id":"assistant-2"}]}}`)
	}

	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: payload}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketCompactionCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketCompactionCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketCompactionCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestNormalizeResponsesWebsocketRequestCreate(t *testing.T) {
	raw := []byte(`{"type":"response.create","model":"test-model","stream":false,"input":[{"type":"message","id":"msg-1"}]}`)

	normalized, last, errMsg := normalizeResponsesWebsocketRequest(raw, nil, nil)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "type").Exists() {
		t.Fatalf("normalized create request must not include type field")
	}
	if !gjson.GetBytes(normalized, "stream").Bool() {
		t.Fatalf("normalized create request must force stream=true")
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}
	if !bytes.Equal(last, normalized) {
		t.Fatalf("last request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestCreateCanonicalizesStringInput(t *testing.T) {
	raw := []byte(`{"type":"response.create","model":"test-model","stream":false,"input":"hello"}`)

	normalized, last, errMsg := normalizeResponsesWebsocketRequest(raw, nil, nil)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	input := gjson.GetBytes(normalized, "input")
	if !input.IsArray() {
		t.Fatalf("input must be normalized to array: %s", normalized)
	}
	if got := input.Get("0.type").String(); got != "message" {
		t.Fatalf("input.0.type = %q, want message", got)
	}
	if got := input.Get("0.role").String(); got != "user" {
		t.Fatalf("input.0.role = %q, want user", got)
	}
	if got := input.Get("0.content.0.text").String(); got != "hello" {
		t.Fatalf("input.0.content.0.text = %q, want hello", got)
	}
	if !bytes.Equal(last, normalized) {
		t.Fatalf("last request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestCreateWithHistory(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "type").Exists() {
		t.Fatalf("normalized subsequent create request must not include type field")
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("merged input len = %d, want 4", len(input))
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "fc-1" ||
		input[2].Get("id").String() != "assistant-1" ||
		input[3].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged input order")
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestWithStringHistoryMergesToolOutput(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":"run tool"}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"}
	]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1","output":"ok"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(input), normalized)
	}
	if got := input[0].Get("content.0.text").String(); got != "run tool" {
		t.Fatalf("first input text = %q, want run tool", got)
	}
	if input[1].Get("call_id").String() != "call-1" || input[2].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged tool items: %s", normalized)
	}
}

func TestNormalizeResponsesWebsocketRequestWithPreviousResponseIDIncremental(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, true, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "type").Exists() {
		t.Fatalf("normalized request must not include type field")
	}
	if gjson.GetBytes(normalized, "previous_response_id").String() != "resp_1" {
		t.Fatalf("previous_response_id must be preserved in incremental mode")
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 {
		t.Fatalf("incremental input len = %d, want 1", len(input))
	}
	if input[0].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected incremental input item id: %s", input[0].Get("id").String())
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}
	if gjson.GetBytes(normalized, "instructions").String() != "be helpful" {
		t.Fatalf("unexpected instructions: %s", gjson.GetBytes(normalized, "instructions").String())
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestInjectsPreviousResponseIDForIncremental(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithLastResponseID(raw, lastRequest, lastResponseOutput, "resp-1", true, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if got := gjson.GetBytes(normalized, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %q, want resp-1", got)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 {
		t.Fatalf("incremental input len = %d, want 1: %s", len(input), normalized)
	}
	if input[0].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected incremental input item id: %s", input[0].Get("id").String())
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}
	if gjson.GetBytes(normalized, "instructions").String() != "be helpful" {
		t.Fatalf("unexpected instructions: %s", gjson.GetBytes(normalized, "instructions").String())
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestInjectsPreviousResponseIDWhenPendingOutputIsPresent(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithIncrementalState(raw, lastRequest, lastResponseOutput, "resp-1", []string{"call-1"}, true, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if got := gjson.GetBytes(normalized, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %q, want resp-1", got)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 || input[0].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected incremental input: %s", normalized)
	}
}

func TestNormalizeResponsesWebsocketRequestSkipsPreviousResponseIDWhenPendingOutputIsMissing(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","role":"user","id":"summary-1","content":"compacted summary"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithIncrementalState(raw, lastRequest, lastResponseOutput, "resp-1", []string{"call-1"}, true, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not be injected when pending tool output is missing: %s", normalized)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 {
		t.Fatalf("replacement input len = %d, want 1: %s", len(input), normalized)
	}
	if input[0].Get("id").String() != "summary-1" {
		t.Fatalf("unexpected replacement input: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestReplacesCodexLocalCompactionTranscript(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.6-sol","stream":true,"instructions":"be helpful","input":[
		{"type":"message","role":"user","id":"old-user","content":[{"type":"input_text","text":"old prompt"}]},
		{"type":"function_call_output","id":"old-tool-output","call_id":"old-call","output":"old result"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"old-tool-call","call_id":"old-call","name":"lookup","arguments":"{}"},
		{"type":"message","role":"assistant","id":"old-assistant","content":[{"type":"output_text","text":"old answer"}]}
	]`)
	raw := []byte(fmt.Sprintf(`{"type":"response.create","input":[
		{"type":"additional_tools","role":"developer","tools":[]},
		{"role":"developer","id":"initial-context","content":"workspace context"},
		{"type":"message","role":"user","id":"compacted-user","content":[{"type":"input_text","text":"retained context"}]},
		{"role":"user","id":"local-summary","content":%q},
		{"type":"message","role":"developer","id":"turn-context","content":[{"type":"input_text","text":"current workspace context"}]},
		{"role":"user","id":"incoming-user","content":"continue the task"}
	],"parallel_tool_calls":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`, codexLocalCompactionSummaryPrefix+"\nThe compacted summary."))

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("replacement request must not include previous_response_id: %s", normalized)
	}
	if got, want := gjson.GetBytes(normalized, "input").Raw, gjson.GetBytes(raw, "input").Raw; got != want {
		t.Fatalf("replacement input did not preserve the complete new transcript:\n got: %s\nwant: %s", got, want)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	wantIDs := []string{"", "initial-context", "compacted-user", "local-summary", "turn-context", "incoming-user"}
	if len(input) != len(wantIDs) {
		t.Fatalf("replacement input len = %d, want %d: %s", len(input), len(wantIDs), normalized)
	}
	for index, wantID := range wantIDs {
		if got := input[index].Get("id").String(); got != wantID {
			t.Fatalf("replacement input[%d].id = %q, want %q: %s", index, got, wantID, normalized)
		}
	}
	if got := input[0].Get("type").String(); got != "additional_tools" {
		t.Fatalf("input[0].type = %q, want additional_tools: %s", got, normalized)
	}
	if got := input[0].Get("role").String(); got != "developer" {
		t.Fatalf("input[0].role = %q, want developer: %s", got, normalized)
	}
	if tools := input[0].Get("tools"); !tools.IsArray() || len(tools.Array()) != 0 {
		t.Fatalf("input[0] empty tools array was not preserved: %s", normalized)
	}
	for _, staleID := range []string{"old-user", "old-tool-output", "old-tool-call", "old-assistant"} {
		if bytes.Contains(normalized, []byte(staleID)) {
			t.Fatalf("replacement input contains stale item %q: %s", staleID, normalized)
		}
	}
	if got := gjson.GetBytes(normalized, "model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("model = %q, want gpt-5.6-sol", got)
	}
	if got := gjson.GetBytes(normalized, "instructions").String(); got != "be helpful" {
		t.Fatalf("instructions = %q, want be helpful", got)
	}
	if !gjson.GetBytes(normalized, "stream").Bool() {
		t.Fatalf("stream must be enabled: %s", normalized)
	}
	if !gjson.GetBytes(normalized, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls was not preserved: %s", normalized)
	}
	if got := gjson.GetBytes(normalized, "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite").String(); got != "true" {
		t.Fatalf("Responses Lite client metadata = %q, want true: %s", got, normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestShouldReplaceWebsocketTranscriptCodexLocalCompactionSemantics(t *testing.T) {
	compactedInput := gjson.Parse(fmt.Sprintf(`[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"initial context"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"retained context"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}
	]`, codexLocalCompactionSummaryPrefix+"\nSummary body."))
	if !shouldReplaceWebsocketTranscript([]byte(`{"type":"response.create"}`), compactedInput) {
		t.Fatal("Codex local compaction input must replace the websocket transcript")
	}
	for _, request := range []string{
		`{"type":"response.create","previous_response_id":"resp-1"}`,
		`{"type":"response.create","previous_response_id":""}`,
		`{"type":"response.create","previous_response_id":null}`,
	} {
		if shouldReplaceWebsocketTranscript([]byte(request), compactedInput) {
			t.Fatalf("request carrying previous_response_id must not use the local compaction rule: %s", request)
		}
	}
	if shouldReplaceWebsocketTranscript([]byte(`{"type":"response.append"}`), compactedInput) {
		t.Fatal("response.append must not be treated as a full local compaction reset")
	}

	ordinaryInput := gjson.Parse(`[
		{"type":"message","role":"developer","content":"Please summarize future messages."},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"Please create a compacted summary of this text."}]}
	]`)
	if shouldReplaceWebsocketTranscript([]byte(`{"type":"response.create"}`), ordinaryInput) {
		t.Fatal("ordinary user/developer input must not replace the transcript")
	}
}

func TestCodexLocalCompactionSummaryContentShapes(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "string content", content: fmt.Sprintf(`%q`, codexLocalCompactionSummaryPrefix+"\nSummary body."), want: true},
		{name: "multiple input text parts", content: fmt.Sprintf(`[{"type":"input_text","text":%q},{"type":"input_text","text":"\nSummary body."}]`, codexLocalCompactionSummaryPrefix), want: true},
		{name: "non-text part before summary", content: fmt.Sprintf(`[{"type":"input_image","image_url":"data:image/png;base64,AA=="},{"type":"input_text","text":%q}]`, codexLocalCompactionSummaryPrefix+"\nSummary body."), want: true},
		{name: "bare prefix", content: fmt.Sprintf(`%q`, codexLocalCompactionSummaryPrefix), want: false},
		{name: "prefix followed by space", content: fmt.Sprintf(`%q`, codexLocalCompactionSummaryPrefix+" Summary body."), want: false},
		{name: "summary after ordinary text", content: fmt.Sprintf(`[{"type":"input_text","text":"ordinary text"},{"type":"input_text","text":%q}]`, codexLocalCompactionSummaryPrefix+"\nSummary body."), want: false},
		{name: "developer summary", content: fmt.Sprintf(`%q`, codexLocalCompactionSummaryPrefix+"\nSummary body."), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			role := "user"
			if test.name == "developer summary" {
				role = "developer"
			}
			input := gjson.Parse(fmt.Sprintf(`[{"type":"message","role":%q,"content":%s}]`, role, test.content))
			if got := inputHasCodexLocalCompactionSummary(input); got != test.want {
				t.Fatalf("inputHasCodexLocalCompactionSummary() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCodexLocalCompactionSummaryAdditionalToolsConstraints(t *testing.T) {
	summary := fmt.Sprintf(`{"role":"user","content":%q}`, codexLocalCompactionSummaryPrefix+"\nSummary body.")
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "Responses Lite tools first", input: fmt.Sprintf(`[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]},%s]`, summary), want: true},
		{name: "tools after message", input: fmt.Sprintf(`[%s,{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]}]`, summary)},
		{name: "tools with user role", input: fmt.Sprintf(`[{"type":"additional_tools","role":"user","tools":[{"type":"custom","name":"exec"}]},%s]`, summary)},
		{name: "tools missing array", input: fmt.Sprintf(`[{"type":"additional_tools","role":"developer"},%s]`, summary)},
		{name: "tools not array", input: fmt.Sprintf(`[{"type":"additional_tools","role":"developer","tools":{}},%s]`, summary)},
		{name: "tools empty", input: fmt.Sprintf(`[{"type":"additional_tools","role":"developer","tools":[]},%s]`, summary), want: true},
		{name: "malformed tool", input: fmt.Sprintf(`[{"type":"additional_tools","role":"developer","tools":[null]},%s]`, summary)},
		{name: "arbitrary input item", input: fmt.Sprintf(`[{"type":"unknown","role":"developer"},%s]`, summary)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := inputHasCodexLocalCompactionSummary(gjson.Parse(test.input)); got != test.want {
				t.Fatalf("inputHasCodexLocalCompactionSummary() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCodexLocalCompactionSummaryRejectsOrdinaryHistoryItems(t *testing.T) {
	tests := []struct {
		name        string
		historyItem string
		wantReplace bool
	}{
		{name: "reasoning", historyItem: `{"type":"reasoning","id":"reasoning-1"}`},
		{name: "assistant", historyItem: `{"type":"message","role":"assistant","id":"assistant-1"}`, wantReplace: true},
		{name: "function call", historyItem: `{"type":"function_call","call_id":"call-1"}`, wantReplace: true},
		{name: "function call output", historyItem: `{"type":"function_call_output","call_id":"call-1"}`},
		{name: "custom tool call", historyItem: `{"type":"custom_tool_call","call_id":"call-1"}`, wantReplace: true},
		{name: "custom tool call output", historyItem: `{"type":"custom_tool_call_output","call_id":"call-1"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := gjson.Parse(fmt.Sprintf(`[%s,{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}]`, test.historyItem, codexLocalCompactionSummaryPrefix+"\nSummary body."))
			if inputHasCodexLocalCompactionSummary(input) {
				t.Fatal("ordinary transcript history must not match the local user-summary shape")
			}
			if got := shouldReplaceWebsocketTranscript([]byte(`{"type":"response.create"}`), input); got != test.wantReplace {
				t.Fatalf("shouldReplaceWebsocketTranscript() = %t, want %t", got, test.wantReplace)
			}
		})
	}
}

func TestNormalizeResponsesWebsocketRequestWithPreviousResponseIDMergedWhenIncrementalDisabled(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must be removed when incremental mode is disabled")
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("merged input len = %d, want 4", len(input))
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "fc-1" ||
		input[2].Get("id").String() != "assistant-1" ||
		input[3].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged input order")
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestCopiesToolFieldsWhenIncrementalDisabled(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"tools":[{"type":"function","name":"exec_command","description":"Run a shell command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}],"tool_choice":"required","parallel_tool_calls":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"call-1","output":"ok"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if got := gjson.GetBytes(normalized, "tool_choice").String(); got != "required" {
		t.Fatalf("tool_choice = %q, want required", got)
	}
	if !gjson.GetBytes(normalized, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls must be copied from last request")
	}
	if got := gjson.GetBytes(normalized, "tools.0.name").String(); got != "exec_command" {
		t.Fatalf("tools.0.name = %q, want exec_command", got)
	}
}

func TestNormalizeResponsesWebsocketRequestMergesInvalidPreviousResponseID(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant"}
	]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"202605140754182a204d2863c14f1f","input":[{"type":"message","id":"msg-2"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, true, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must be removed for full transcript replay: %s", normalized)
	}
	input := gjson.GetBytes(normalized, "input")
	if got := len(input.Array()); got != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", got, normalized)
	}
	if input.Get("0.id").String() != "msg-1" ||
		input.Get("1.id").String() != "assistant-1" ||
		input.Get("2.id").String() != "msg-2" {
		t.Fatalf("unexpected replay transcript: %s", input.Raw)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestAppend(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1"},
		{"type":"function_call_output","id":"tool-out-1"}
	]`)
	raw := []byte(`{"type":"response.append","input":[{"type":"message","id":"msg-2"},{"type":"message","id":"msg-3"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 5 {
		t.Fatalf("merged input len = %d, want 5", len(input))
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "assistant-1" ||
		input[2].Get("id").String() != "tool-out-1" ||
		input[3].Get("id").String() != "msg-2" ||
		input[4].Get("id").String() != "msg-3" {
		t.Fatalf("unexpected merged input order")
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized append request")
	}
}

func TestNormalizeResponsesWebsocketRequestAppendWithoutCreate(t *testing.T) {
	raw := []byte(`{"type":"response.append","input":[]}`)

	_, _, errMsg := normalizeResponsesWebsocketRequest(raw, nil, nil)
	if errMsg == nil {
		t.Fatalf("expected error for append without previous request")
	}
	if errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusBadRequest)
	}
}

func TestWebsocketJSONPayloadsFromChunk(t *testing.T) {
	chunk := []byte("event: response.created\n\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\ndata: [DONE]\n")

	payloads := websocketJSONPayloadsFromChunk(chunk)
	if len(payloads) != 1 {
		t.Fatalf("payloads len = %d, want 1", len(payloads))
	}
	if gjson.GetBytes(payloads[0], "type").String() != "response.created" {
		t.Fatalf("unexpected payload type: %s", gjson.GetBytes(payloads[0], "type").String())
	}
}

func TestWebsocketJSONPayloadsFromPlainJSONChunk(t *testing.T) {
	chunk := []byte(`{"type":"response.completed","response":{"id":"resp-1"}}`)

	payloads := websocketJSONPayloadsFromChunk(chunk)
	if len(payloads) != 1 {
		t.Fatalf("payloads len = %d, want 1", len(payloads))
	}
	if gjson.GetBytes(payloads[0], "type").String() != "response.completed" {
		t.Fatalf("unexpected payload type: %s", gjson.GetBytes(payloads[0], "type").String())
	}
}

func TestResponseCompletedOutputFromPayload(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"message","id":"out-1"}]}}`)

	output := responseCompletedOutputFromPayload(payload, nil, nil)
	items := gjson.ParseBytes(output).Array()
	if len(items) != 1 {
		t.Fatalf("output len = %d, want 1", len(items))
	}
	if items[0].Get("id").String() != "out-1" {
		t.Fatalf("unexpected output id: %s", items[0].Get("id").String())
	}
}

func TestRestoreResponsesWebsocketCompletionOutputPreservesNonEmptyOutput(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"message","id":"out-1"}]}}`)
	collector := map[int64][]byte{0: []byte(`{"type":"function_call","id":"call-1","call_id":"call-1"}`)}

	restored := restoreResponsesWebsocketCompletionOutput(payload, collector, nil)
	if string(restored) != string(payload) {
		t.Fatalf("non-empty completion output was overwritten: %s", restored)
	}
}

func TestResponsesWebsocketOutputAccumulator(t *testing.T) {
	acc := newResponsesWebsocketOutputAccumulator()
	acc.AppendOutputItemDone([]byte(`{"type":"response.output_item.done","item":{"type":"message","id":"out-1"}}`))
	acc.AppendOutputItemDone([]byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"shell","arguments":"{}"}}`))
	if got := acc.Count(); got != 2 {
		t.Fatalf("Count() = %d, want 2", got)
	}
	output := acc.Output()
	if got := gjson.GetBytes(output, "0.id").String(); got != "out-1" {
		t.Fatalf("output[0].id = %q, want out-1; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "1.call_id").String(); got != "call-1" {
		t.Fatalf("output[1].call_id = %q, want call-1; output=%s", got, output)
	}
	acc.SetCompleted([]byte(`{"type":"response.completed","response":{"output":[{"type":"message","id":"final"}]}}`))
	if got := acc.Count(); got != 1 {
		t.Fatalf("Count() after completed = %d, want 1", got)
	}
	if got := gjson.GetBytes(acc.Output(), "0.id").String(); got != "final" {
		t.Fatalf("completed output id = %q, want final; output=%s", got, acc.Output())
	}
}

func TestAppendWebsocketEvent(t *testing.T) {
	var builder strings.Builder

	appendWebsocketEvent(&builder, "request", []byte("  {\"type\":\"response.create\"}\n"))
	appendWebsocketEvent(&builder, "response", []byte("{\"type\":\"response.created\"}"))

	got := builder.String()
	if !strings.Contains(got, "websocket.request\n{\"type\":\"response.create\"}\n") {
		t.Fatalf("request event not found in body: %s", got)
	}
	if !strings.Contains(got, "websocket.response\n{\"type\":\"response.created\"}\n") {
		t.Fatalf("response event not found in body: %s", got)
	}
}

func TestAppendWebsocketTimelineEvent(t *testing.T) {
	var builder strings.Builder
	ts := time.Date(2026, time.April, 1, 12, 34, 56, 789000000, time.UTC)

	appendWebsocketTimelineEvent(&builder, "request", []byte("  {\"type\":\"response.create\"}\n"), ts)

	got := builder.String()
	if !strings.Contains(got, "Timestamp: 2026-04-01T12:34:56.789Z") {
		t.Fatalf("timeline timestamp not found: %s", got)
	}
	if !strings.Contains(got, "Event: websocket.request") {
		t.Fatalf("timeline event not found: %s", got)
	}
	if !strings.Contains(got, "{\"type\":\"response.create\"}") {
		t.Fatalf("timeline payload not found: %s", got)
	}
}

func TestAppendWebsocketTimelineEventTruncatesLargePayloads(t *testing.T) {
	var builder strings.Builder
	ts := time.Date(2026, time.April, 1, 12, 34, 56, 789000000, time.UTC)
	payload := bytes.Repeat([]byte("a"), responsesWebsocketTimelinePayloadMaxBytes+1024)

	appendWebsocketTimelineEvent(&builder, "response", payload, ts)

	got := builder.String()
	if !strings.Contains(got, "... websocket payload truncated ...") {
		t.Fatalf("timeline payload truncation marker not found")
	}
	if builder.Len() > responsesWebsocketTimelinePayloadMaxBytes+512 {
		t.Fatalf("timeline event grew too large: %d", builder.Len())
	}
}

func TestAppendWebsocketTimelineEventCapsTotalSize(t *testing.T) {
	var builder strings.Builder
	ts := time.Date(2026, time.April, 1, 12, 34, 56, 789000000, time.UTC)
	payload := bytes.Repeat([]byte("a"), responsesWebsocketTimelinePayloadMaxBytes)

	for builder.Len() < responsesWebsocketTimelineMaxBytes+responsesWebsocketTimelinePayloadMaxBytes {
		before := builder.Len()
		appendWebsocketTimelineEvent(&builder, "response", payload, ts)
		if builder.Len() == before {
			break
		}
	}

	if builder.Len() > responsesWebsocketTimelineMaxBytes {
		t.Fatalf("timeline length = %d, want <= %d", builder.Len(), responsesWebsocketTimelineMaxBytes)
	}
	before := builder.Len()
	appendWebsocketTimelineEvent(&builder, "response", payload, ts)
	if builder.Len() != before {
		t.Fatalf("timeline grew after cap: before=%d after=%d", before, builder.Len())
	}
}

func TestSetWebsocketTimelineBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	setWebsocketTimelineBody(c, " \n ")
	if _, exists := c.Get(wsTimelineBodyKey); exists {
		t.Fatalf("timeline body key should not be set for empty body")
	}

	setWebsocketTimelineBody(c, "timeline body")
	value, exists := c.Get(wsTimelineBodyKey)
	if !exists {
		t.Fatalf("timeline body key not set")
	}
	bodyBytes, ok := value.([]byte)
	if !ok {
		t.Fatalf("timeline body key type mismatch")
	}
	if string(bodyBytes) != "timeline body" {
		t.Fatalf("timeline body = %q, want %q", string(bodyBytes), "timeline body")
	}
}

func TestWebsocketTimelineLogFallsBackToMemoryWithoutSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ts := time.Date(2026, time.April, 1, 12, 34, 56, 789000000, time.UTC)

	timelineLog := newWebsocketTimelineLog(true, nil)
	timelineLog.BeginRequest()
	timelineLog.Append("request", []byte(`{"type":"response.create"}`), ts)
	timelineLog.SetContext(c)

	value, exists := c.Get(wsTimelineBodyKey)
	if !exists {
		t.Fatalf("timeline body key not set")
	}
	bodyBytes, ok := value.([]byte)
	if !ok {
		t.Fatalf("timeline body key type mismatch")
	}
	got := string(bodyBytes)
	if !strings.Contains(got, "Event: websocket.request") {
		t.Fatalf("timeline event not found: %s", got)
	}
	if !strings.Contains(got, `{"type":"response.create"}`) {
		t.Fatalf("timeline payload not found: %s", got)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedOutput(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	cacheWarm := []byte(`{"previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"call-1","output":"ok"}]}`)
	warmed := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, cacheWarm)
	if gjson.GetBytes(warmed, "input.0.call_id").String() != "call-1" {
		t.Fatalf("expected warmup output to remain")
	}

	raw := []byte(`{"input":[{"type":"function_call","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "function_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected first item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "function_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted output: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanFunctionCall(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"function_call","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsFunctionCallWithEmptyName(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"function_call","call_id":"call-1","name":""},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsInvalidFunctionArgumentsAndOutput(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"function_call","call_id":"call-invalid","name":"exec_command","arguments":"not-json"},{"type":"function_call_output","call_id":"call-invalid","output":"invalid result"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOutputForCachedInvalidFunctionArguments(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-invalid", []byte(`{"type":"function_call","call_id":"call-invalid","name":"exec_command","arguments":"not-json"}`))
	raw := []byte(`{"input":[{"type":"function_call_output","call_id":"call-invalid","output":"invalid result"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestSanitizeResponsesInputToolCallNamesDropsEmptyNameAndOutput(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","id":"msg-1"},{"type":"function_call","id":"fc-1","call_id":"call-1","name":"","arguments":"{}"},{"type":"function_call_output","id":"out-1","call_id":"call-1","output":"ok"},{"type":"function_call","id":"fc-2","call_id":"call-2","name":"exec_command","arguments":"{}"},{"type":"function_call_output","id":"out-2","call_id":"call-2","output":"done"}]}`)

	sanitized := sanitizeResponsesInputToolCallHistory(sanitizeResponsesInputToolCallNames(raw))

	items := gjson.GetBytes(sanitized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("sanitized input len = %d, want 3: %s", len(items), sanitized)
	}
	if items[0].Get("id").String() != "msg-1" || items[1].Get("call_id").String() != "call-2" || items[2].Get("call_id").String() != "call-2" {
		t.Fatalf("unexpected sanitized input: %s", sanitized)
	}
	if strings.Contains(string(sanitized), `"name":""`) || strings.Contains(string(sanitized), `"call_id":"call-1"`) {
		t.Fatalf("invalid empty-name call leaked through: %s", sanitized)
	}
}

func TestSanitizeResponsesInputToolCallNamesDropsInvalidFunctionArgumentsAndOutput(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","id":"msg-1"},{"type":"function_call","id":"fc-invalid","call_id":"call-invalid","name":"exec_command","arguments":"not-json"},{"type":"function_call_output","id":"out-invalid","call_id":"call-invalid","output":"invalid result"},{"type":"function_call","id":"fc-valid","call_id":"call-valid","name":"exec_command","arguments":"{}"},{"type":"function_call_output","id":"out-valid","call_id":"call-valid","output":"valid result"},{"type":"custom_tool_call","id":"ctc-freeform","call_id":"call-freeform","name":"shell","arguments":"echo hello"},{"type":"custom_tool_call_output","id":"out-freeform","call_id":"call-freeform","output":"hello"}]}`)

	sanitized := sanitizeResponsesInputToolCallHistory(sanitizeResponsesInputToolCallNames(raw))

	items := gjson.GetBytes(sanitized, "input").Array()
	if len(items) != 5 {
		t.Fatalf("sanitized input len = %d, want 5: %s", len(items), sanitized)
	}
	if items[1].Get("call_id").String() != "call-valid" || items[2].Get("call_id").String() != "call-valid" || items[3].Get("call_id").String() != "call-freeform" || items[4].Get("call_id").String() != "call-freeform" {
		t.Fatalf("unexpected sanitized input: %s", sanitized)
	}
	if strings.Contains(string(sanitized), "call-invalid") || strings.Contains(string(sanitized), "not-json") {
		t.Fatalf("invalid function-call arguments leaked through: %s", sanitized)
	}
}

func TestSanitizeResponsesInputToolCallNamesDropsUnpairedToolHistory(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","id":"msg-1"},{"type":"function_call_output","id":"out-orphan","call_id":"call-orphan","output":"missing call"},{"type":"function_call","id":"fc-unanswered","call_id":"call-unanswered","name":"exec_command","arguments":"{}"},{"type":"function_call","id":"fc-ok","call_id":"call-ok","name":"exec_command","arguments":"{}"},{"type":"function_call_output","id":"out-ok","call_id":"call-ok","output":"done"},{"type":"message","id":"msg-2"}]}`)

	sanitized := sanitizeResponsesInputToolCallHistory(sanitizeResponsesInputToolCallNames(raw))

	items := gjson.GetBytes(sanitized, "input").Array()
	if len(items) != 4 {
		t.Fatalf("sanitized input len = %d, want 4: %s", len(items), sanitized)
	}
	if items[0].Get("id").String() != "msg-1" || items[1].Get("call_id").String() != "call-ok" || items[2].Get("call_id").String() != "call-ok" || items[3].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected sanitized input: %s", sanitized)
	}
	if strings.Contains(string(sanitized), "call-orphan") || strings.Contains(string(sanitized), "call-unanswered") {
		t.Fatalf("unpaired tool history leaked through: %s", sanitized)
	}
}

func TestSanitizeResponsesInputToolCallNamesRewritesFunctionCallIDPrefix(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","id":"msg-1"},{"type":"function_call","id":"call_c24e167877134ea69a7d2a9d","call_id":"call_c24e167877134ea69a7d2a9d","name":"exec_command","arguments":"{}"},{"type":"function_call_output","id":"out-1","call_id":"call_c24e167877134ea69a7d2a9d","output":"ok"},{"type":"function_call","id":"fc-keep","call_id":"call-keep","name":"exec_command","arguments":"{}"},{"type":"function_call_output","id":"out-keep","call_id":"call-keep","output":"done"}]}`)

	sanitized := sanitizeResponsesInputToolCallNames(raw)

	items := gjson.GetBytes(sanitized, "input").Array()
	if len(items) != 5 {
		t.Fatalf("sanitized input len = %d, want 5: %s", len(items), sanitized)
	}
	if got := items[1].Get("id").String(); got != "fc_c24e167877134ea69a7d2a9d" {
		t.Fatalf("function_call id = %q, want fc_c24e167877134ea69a7d2a9d: %s", got, sanitized)
	}
	if got := items[1].Get("call_id").String(); got != "call_c24e167877134ea69a7d2a9d" {
		t.Fatalf("function_call call_id = %q, want unchanged call_c24e167877134ea69a7d2a9d: %s", got, sanitized)
	}
	if got := items[2].Get("call_id").String(); got != "call_c24e167877134ea69a7d2a9d" {
		t.Fatalf("function_call_output call_id = %q, want unchanged: %s", got, sanitized)
	}
	if got := items[3].Get("id").String(); got != "fc-keep" {
		t.Fatalf("already-valid function_call id should be preserved, got %q: %s", got, sanitized)
	}
	if strings.Contains(string(sanitized), `"id":"call_c24e167877134ea69a7d2a9d"`) {
		t.Fatalf("non-fc function_call id leaked through: %s", sanitized)
	}
}

func TestSanitizeResponsesInputToolCallNamesRewritesCustomToolCallIDPrefix(t *testing.T) {
	raw := []byte(`{"input":[{"type":"message","id":"msg-1"},{"type":"custom_tool_call","id":"call_Fn6VYI71GuLSPcTCqkiZEogO","call_id":"call_Fn6VYI71GuLSPcTCqkiZEogO","name":"shell","arguments":"{}"},{"type":"custom_tool_call_output","call_id":"call_Fn6VYI71GuLSPcTCqkiZEogO","output":"done"}]}`)

	sanitized := sanitizeResponsesInputToolCallNames(raw)

	items := gjson.GetBytes(sanitized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("sanitized input len = %d, want 3: %s", len(items), sanitized)
	}
	if got := items[1].Get("id").String(); got != "fc_Fn6VYI71GuLSPcTCqkiZEogO" {
		t.Fatalf("custom_tool_call id = %q, want fc_Fn6VYI71GuLSPcTCqkiZEogO: %s", got, sanitized)
	}
	if got := items[1].Get("type").String(); got != "custom_tool_call" {
		t.Fatalf("custom_tool_call type not preserved: %s", sanitized)
	}
	if got := items[1].Get("call_id").String(); got != "call_Fn6VYI71GuLSPcTCqkiZEogO" {
		t.Fatalf("custom_tool_call call_id = %q, want unchanged: %s", got, sanitized)
	}
	if got := items[2].Get("call_id").String(); got != "call_Fn6VYI71GuLSPcTCqkiZEogO" {
		t.Fatalf("custom_tool_call_output call_id = %q, want unchanged: %s", got, sanitized)
	}
	if strings.Contains(string(sanitized), `"id":"call_Fn6VYI71GuLSPcTCqkiZEogO"`) {
		t.Fatalf("non-fc custom_tool_call id leaked through: %s", sanitized)
	}
}

func TestNormalizeResponsesToolCallItemIDPreservesValidPrefixes(t *testing.T) {
	cases := []struct {
		name string
		item string
		want string
	}{
		{name: "fc underscore prefix preserved", item: `{"type":"function_call","id":"fc_abc","call_id":"call_abc","name":"t","arguments":"{}"}`, want: "fc_abc"},
		{name: "fc dash prefix preserved", item: `{"type":"function_call","id":"fc-1","call_id":"call-1","name":"t","arguments":"{}"}`, want: "fc-1"},
		{name: "call underscore rewritten", item: `{"type":"function_call","id":"call_c24e","call_id":"call_c24e","name":"t","arguments":"{}"}`, want: "fc_c24e"},
		{name: "codex internal function id rewritten", item: `{"type":"function_call","id":"functions_exec_command_0_7ca4ee899e52361f","call_id":"call_abc","name":"exec_command","arguments":"{}"}`, want: "fc_functions_exec_command_0_7ca4ee899e52361f"},
		{name: "bare id left alone", item: `{"type":"function_call","id":"abc","call_id":"call_abc","name":"t","arguments":"{}"}`, want: "abc"},
		{name: "ctc dash id left alone", item: `{"type":"custom_tool_call","id":"ctc-compact","call_id":"call-1","name":"apply_patch","arguments":"{}"}`, want: "ctc-compact"},
		{name: "empty id left alone", item: `{"type":"function_call","id":"","call_id":"call_x","name":"t","arguments":"{}"}`, want: ""},
		{name: "custom_tool_call fc underscore preserved", item: `{"type":"custom_tool_call","id":"fc_abc","call_id":"call_abc","name":"shell","arguments":"{}"}`, want: "fc_abc"},
		{name: "custom_tool_call call underscore rewritten", item: `{"type":"custom_tool_call","id":"call_Fn6VYI71","call_id":"call_Fn6VYI71","name":"shell","arguments":"{}"}`, want: "fc_Fn6VYI71"},
		{name: "custom_tool_call codex internal id rewritten", item: `{"type":"custom_tool_call","id":"functions_exec_command_0_7ca4ee899e52361f","call_id":"call_Fn6VYI71","name":"exec_command","arguments":"{}"}`, want: "fc_functions_exec_command_0_7ca4ee899e52361f"},
		{name: "custom_tool_call bare id left alone", item: `{"type":"custom_tool_call","id":"abc","call_id":"call_abc","name":"shell","arguments":"{}"}`, want: "abc"},
		{name: "custom_tool_call empty id left alone", item: `{"type":"custom_tool_call","id":"","call_id":"call_x","name":"shell","arguments":"{}"}`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updated, ok := normalizeResponsesToolCallItemID(json.RawMessage(tc.item))
			if !ok && tc.want != gjson.GetBytes([]byte(tc.item), "id").String() {
				t.Fatalf("expected rewrite, got ok=false for %s", tc.item)
			}
			if got := gjson.GetBytes(updated, "id").String(); got != tc.want {
				t.Fatalf("id = %q, want %q: %s", got, tc.want, updated)
			}
		})
	}
}

func TestNormalizeResponsesWebsocketPassthroughRequestNormalizesCodexCustomToolCallID(t *testing.T) {
	raw := []byte(`{"type":"response.create","model":"gpt-5.4","input":[{"type":"custom_tool_call","id":"functions_exec_command_0_7ca4ee899e52361f","call_id":"call_abc","name":"exec_command","arguments":"{}"},{"type":"custom_tool_call_output","call_id":"call_abc","output":"ok"}]}`)

	normalized, errMsg := normalizeResponsesWebsocketPassthroughRequest(raw, "")
	if errMsg != nil {
		t.Fatalf("unexpected passthrough normalization error: %v", errMsg.Error)
	}
	if got := gjson.GetBytes(normalized, "input.0.id").String(); got != "fc_functions_exec_command_0_7ca4ee899e52361f" {
		t.Fatalf("custom_tool_call id = %q, want fc-prefixed ID: %s", got, normalized)
	}
}

func TestNormalizeResponsesWebsocketPassthroughRequestDropsEmptyToolCallID(t *testing.T) {
	raw := []byte(`{"type":"response.create","model":"gpt-5.4","previous_response_id":"resp_1","input":[{"type":"function_call","id":"fc-empty","call_id":"","name":"exec_command","arguments":"{}"},{"type":"custom_tool_call","id":"fc-blank","call_id":"  ","name":"apply_patch","input":"broken"},{"type":"function_call_output","call_id":"","output":"broken"},{"type":"custom_tool_call_output","call_id":"  ","output":"also broken"},{"type":"function_call_output","call_id":"call_live","output":"ok"}]}`)

	normalized, errMsg := normalizeResponsesWebsocketPassthroughRequest(raw, "")
	if errMsg != nil {
		t.Fatalf("unexpected passthrough normalization error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1: %s", len(input), normalized)
	}
	if got := input[0].Get("call_id").String(); got != "call_live" {
		t.Fatalf("remaining call_id = %q, want call_live: %s", got, normalized)
	}
}

func TestBuildPassthroughTranscriptReplayPayloadDropsUnpairedToolHistory(t *testing.T) {
	clientPayload := []byte(`{"type":"response.create","model":"gpt-5.4","previous_response_id":"resp_missing","input":[{"type":"message","role":"user","content":"next"}]}`)
	accumulatedInput := []byte(`[{"type":"message","role":"user","content":"start"},{"type":"function_call_output","call_id":"","output":"broken"},{"type":"function_call_output","call_id":"call_orphan","output":"orphan"},{"type":"function_call","id":"fc_ok","call_id":"call_ok","name":"exec_command","arguments":"{}"},{"type":"function_call_output","call_id":"call_ok","output":"done"}]`)

	replayed, err := buildPassthroughTranscriptReplayPayload(clientPayload, accumulatedInput, "")
	if err != nil {
		t.Fatalf("build replay payload: %v", err)
	}

	input := gjson.GetBytes(replayed, "input").Array()
	if len(input) != 3 {
		t.Fatalf("input len = %d, want 3: %s", len(input), replayed)
	}
	if input[0].Get("type").String() != "message" || input[1].Get("call_id").String() != "call_ok" || input[2].Get("call_id").String() != "call_ok" {
		t.Fatalf("unexpected replay input: %s", replayed)
	}
	if gjson.GetBytes(replayed, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id should be removed: %s", replayed)
	}
}

func TestNormalizeResponsesWebsocketPassthroughRequestNormalizesCodexFunctionCallID(t *testing.T) {
	raw := []byte(`{"type":"response.create","model":"gpt-5.4","input":[{"type":"function_call","id":"functions_exec_command_0_7ca4ee899e52361f","call_id":"call_abc","name":"exec_command","arguments":"{}"},{"type":"function_call_output","call_id":"call_abc","output":"ok"},{"type":"custom_tool_call","id":"call_custom","call_id":"call_custom","name":"apply_patch","input":"*** Begin Patch"},{"type":"custom_tool_call_output","call_id":"call_custom","output":"done"}]}`)

	normalized, errMsg := normalizeResponsesWebsocketPassthroughRequest(raw, "")
	if errMsg != nil {
		t.Fatalf("unexpected passthrough normalization error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("input len = %d, want 4: %s", len(input), normalized)
	}
	if got := input[0].Get("id").String(); got != "fc_functions_exec_command_0_7ca4ee899e52361f" {
		t.Fatalf("function_call id = %q, want fc-prefixed ID: %s", got, normalized)
	}
	if got := input[0].Get("call_id").String(); got != "call_abc" {
		t.Fatalf("function_call call_id = %q, want unchanged call_abc: %s", got, normalized)
	}
	if got := input[1].Get("call_id").String(); got != "call_abc" {
		t.Fatalf("function_call_output call_id = %q, want unchanged call_abc: %s", got, normalized)
	}
	if got := input[2].Get("id").String(); got != "call_custom" {
		t.Fatalf("custom_tool_call id = %q, want unchanged call_custom: %s", got, normalized)
	}
	if got := input[2].Get("call_id").String(); got != "call_custom" {
		t.Fatalf("custom_tool_call call_id = %q, want unchanged call_custom: %s", got, normalized)
	}
	if got := input[3].Get("call_id").String(); got != "call_custom" {
		t.Fatalf("custom_tool_call_output call_id = %q, want unchanged call_custom: %s", got, normalized)
	}
}

func TestBuildPassthroughTranscriptReplayPayloadNormalizesCodexFunctionCallID(t *testing.T) {
	clientPayload := []byte(`{"type":"response.create","previous_response_id":"resp_abc","input":[{"type":"message","role":"user","content":"continue"}]}`)
	accumulatedInput := []byte(`[{"type":"function_call","id":"functions_exec_command_0_7ca4ee899e52361f","call_id":"call_abc","name":"exec_command","arguments":"{}"},{"type":"function_call_output","call_id":"call_abc","output":"ok"},{"type":"custom_tool_call","id":"call_custom","call_id":"call_custom","name":"apply_patch","input":"*** Begin Patch"},{"type":"custom_tool_call_output","call_id":"call_custom","output":"done"}]`)

	replay, err := buildPassthroughTranscriptReplayPayload(clientPayload, accumulatedInput, "gpt-5.4")
	if err != nil {
		t.Fatalf("build replay payload: %v", err)
	}
	if gjson.GetBytes(replay, "previous_response_id").Exists() {
		t.Fatalf("replay must omit previous_response_id: %s", replay)
	}
	if got := gjson.GetBytes(replay, "input.0.id").String(); got != "fc_functions_exec_command_0_7ca4ee899e52361f" {
		t.Fatalf("replay function_call id = %q, want fc-prefixed ID: %s", got, replay)
	}
	if got := gjson.GetBytes(replay, "input.0.call_id").String(); got != "call_abc" {
		t.Fatalf("replay function_call call_id = %q, want unchanged call_abc: %s", got, replay)
	}
	if got := gjson.GetBytes(replay, "input.1.call_id").String(); got != "call_abc" {
		t.Fatalf("replay function_call_output call_id = %q, want unchanged call_abc: %s", got, replay)
	}
	if got := gjson.GetBytes(replay, "input.2.id").String(); got != "call_custom" {
		t.Fatalf("replay custom_tool_call id = %q, want unchanged call_custom: %s", got, replay)
	}
	if got := gjson.GetBytes(replay, "input.2.call_id").String(); got != "call_custom" {
		t.Fatalf("replay custom_tool_call call_id = %q, want unchanged call_custom: %s", got, replay)
	}
	if got := gjson.GetBytes(replay, "input.3.call_id").String(); got != "call_custom" {
		t.Fatalf("replay custom_tool_call_output call_id = %q, want unchanged call_custom: %s", got, replay)
	}
}

func TestNormalizeResponsesWebsocketPreviousResponseIDSanitizesEmptyNameToolCall(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1","role":"user"}]}`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"","arguments":"{}"},{"type":"function_call_output","id":"out-1","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-2","role":"user"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, nil, true, true)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	normalized = sanitizeResponsesInputToolCallNames(normalized)

	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 1 {
		t.Fatalf("sanitized input len = %d, want 1: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected sanitized incremental input: %s", normalized)
	}
	if strings.Contains(string(normalized), `"name":""`) || strings.Contains(string(normalized), `"call_id":"call-1"`) {
		t.Fatalf("empty-name tool call leaked through: %s", normalized)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedCallForOrphanOutput(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"function_call","call_id":"call-1","name":"tool"}`))

	raw := []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "function_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted call: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "function_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsKeepsPreviousResponseOutputIncremental(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"}`))

	raw := []byte(`{"previous_response_id":"resp-latest","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	if got := gjson.GetBytes(repaired, "previous_response_id").String(); got != "resp-latest" {
		t.Fatalf("previous_response_id = %q, want resp-latest", got)
	}
	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 2 {
		t.Fatalf("repaired input len = %d, want 2: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "function_call_output" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "message" || input[1].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[1].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsKeepsPreviousResponseCallIncremental(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	outputCache.record(sessionKey, "call-1", []byte(`{"type":"function_call_output","call_id":"call-1","id":"tool-out-1","output":"ok"}`))

	raw := []byte(`{"previous_response_id":"resp-latest","input":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	if got := gjson.GetBytes(repaired, "previous_response_id").String(); got != "resp-latest" {
		t.Fatalf("previous_response_id = %q, want resp-latest", got)
	}
	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 2 {
		t.Fatalf("repaired input len = %d, want 2: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "function_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected call item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "message" || input[1].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[1].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanOutputWhenCallMissing(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedCustomToolOutput(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	cacheWarm := []byte(`{"previous_response_id":"resp-1","input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"}]}`)
	warmed := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, cacheWarm)
	if gjson.GetBytes(warmed, "input.0.call_id").String() != "call-1" {
		t.Fatalf("expected warmup output to remain")
	}

	raw := []byte(`{"input":[{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "custom_tool_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected first item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "custom_tool_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted output: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanCustomToolCall(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedCustomToolCallForOrphanOutput(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"}`))

	raw := []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "custom_tool_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted call: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "custom_tool_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsKeepsPreviousResponseCustomToolOutputIncremental(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"}`))

	raw := []byte(`{"previous_response_id":"resp-latest","input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	if got := gjson.GetBytes(repaired, "previous_response_id").String(); got != "resp-latest" {
		t.Fatalf("previous_response_id = %q, want resp-latest", got)
	}
	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 2 {
		t.Fatalf("repaired input len = %d, want 2: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "custom_tool_call_output" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "message" || input[1].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[1].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanCustomToolOutputWhenCallMissing(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRecordResponsesWebsocketToolCallsFromPayloadWithCache(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool","arguments":"{}"}]}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, sessionKey, payload)

	cached, ok := cache.get(sessionKey, "call-1")
	if !ok {
		t.Fatalf("expected cached tool call")
	}
	if gjson.GetBytes(cached, "type").String() != "function_call" || gjson.GetBytes(cached, "call_id").String() != "call-1" {
		t.Fatalf("unexpected cached tool call: %s", cached)
	}
}

func TestRecordResponsesWebsocketToolCallsFromPayloadWithCacheSkipsEmptyName(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"","arguments":"{}"}]}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, sessionKey, payload)

	if _, ok := cache.get(sessionKey, "call-1"); ok {
		t.Fatalf("expected empty-name tool call to be skipped")
	}
}

func TestRecordResponsesWebsocketToolCallsFromPayloadWithCacheSkipsInvalidFunctionArguments(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{
			name:    "completed",
			payload: []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool","arguments":"not-json"}]}}`),
		},
		{
			name:    "output item done",
			payload: []byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool","arguments":"not-json"}}`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cache := newWebsocketToolOutputCache(time.Minute, 10)
			recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, "session-1", test.payload)

			if _, ok := cache.get("session-1", "call-1"); ok {
				t.Fatalf("expected invalid function-call arguments to be skipped")
			}
		})
	}
}

func TestRecordResponsesWebsocketCustomToolCallsFromCompletedPayloadWithCache(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch","input":"*** Begin Patch"}]}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, sessionKey, payload)

	cached, ok := cache.get(sessionKey, "call-1")
	if !ok {
		t.Fatalf("expected cached custom tool call")
	}
	if gjson.GetBytes(cached, "type").String() != "custom_tool_call" || gjson.GetBytes(cached, "call_id").String() != "call-1" {
		t.Fatalf("unexpected cached custom tool call: %s", cached)
	}
}

func TestRecordResponsesWebsocketCustomToolCallsFromOutputItemDoneWithCache(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	payload := []byte(`{"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch","input":"*** Begin Patch"}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, sessionKey, payload)

	cached, ok := cache.get(sessionKey, "call-1")
	if !ok {
		t.Fatalf("expected cached custom tool call")
	}
	if gjson.GetBytes(cached, "type").String() != "custom_tool_call" || gjson.GetBytes(cached, "call_id").String() != "call-1" {
		t.Fatalf("unexpected cached custom tool call: %s", cached)
	}
}

func TestForwardResponsesWebsocketRestoresAndForwardsCompletedOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			errClose := conn.Close()
			if errClose != nil {
				serverErrCh <- errClose
			}
		}()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte, 2)
		errCh := make(chan *interfaces.ErrorMessage)
		data <- []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"call-1","call_id":"call-1","name":"lookup","arguments":"{}"}}`)
		data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n")
		close(data)
		close(errCh)

		timelineLog := newInMemoryWebsocketTimelineLog()
		completedOutput, _, _, errMsg, _, err := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
			nil,
			nil,
		)
		if err != nil {
			serverErrCh <- err
			return
		}
		if errMsg != nil {
			serverErrCh <- fmt.Errorf("unexpected websocket error message: %v", errMsg.Error)
			return
		}
		if gjson.GetBytes(completedOutput, "0.id").String() != "call-1" {
			serverErrCh <- errors.New("completed output not restored")
			return
		}
		if !strings.Contains(timelineLog.String(), "Event: websocket.response") {
			serverErrCh <- errors.New("websocket timeline did not capture downstream response")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		errClose := conn.Close()
		if errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	_, outputItemPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read output item websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(outputItemPayload, "type").String(); got != "response.output_item.done" {
		t.Fatalf("output item payload type = %s, want response.output_item.done", got)
	}

	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read completion websocket message: %v", errReadMessage)
	}
	if gjson.GetBytes(payload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("payload type = %s, want %s", gjson.GetBytes(payload, "type").String(), wsEventTypeCompleted)
	}
	if strings.Contains(string(payload), "response.done") {
		t.Fatalf("payload unexpectedly rewrote completed event: %s", payload)
	}
	if got := gjson.GetBytes(payload, "response.output.0.id").String(); got != "call-1" {
		t.Fatalf("downstream completion output id = %q, want call-1; payload=%s", got, payload)
	}

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketSynthesizesCompletedForErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				serverErrCh <- errClose
			}
		}()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte)
		errCh := make(chan *interfaces.ErrorMessage, 1)
		errCh <- &interfaces.ErrorMessage{
			StatusCode: http.StatusTooManyRequests,
			Error:      errors.New("usage limit reached"),
		}

		base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
		h := NewOpenAIResponsesAPIHandler(base)
		timelineLog := newInMemoryWebsocketTimelineLog()
		_, _, _, errMsg, _, err := h.forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
			nil,
			nil,
		)
		if err != nil {
			serverErrCh <- err
			return
		}
		if errMsg == nil {
			serverErrCh <- errors.New("expected websocket error message")
			return
		}
		if !strings.Contains(timelineLog.String(), "\"type\":\"response.output_item.done\"") {
			serverErrCh <- errors.New("websocket timeline did not capture response.output_item.done")
			return
		}
		if !strings.Contains(timelineLog.String(), "\"type\":\"response.completed\"") {
			serverErrCh <- errors.New("websocket timeline did not capture response.completed")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	if errDeadline := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); errDeadline != nil {
		t.Fatalf("set read deadline: %v", errDeadline)
	}
	_, itemPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket output item payload: %v", err)
	}

	if got := gjson.GetBytes(itemPayload, "type").String(); got != "response.output_item.done" {
		t.Fatalf("first payload type = %q, want response.output_item.done; payload=%s", got, itemPayload)
	}
	if !strings.Contains(gjson.GetBytes(itemPayload, "item.content.0.text").String(), "rate_limit_exceeded") {
		t.Fatalf("output item must mention rate_limit_exceeded; payload=%s", itemPayload)
	}
	_, completedPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket completed payload: %v", err)
	}
	if got := gjson.GetBytes(completedPayload, "type").String(); got != "response.completed" {
		t.Fatalf("second payload type = %q, want response.completed; payload=%s", got, completedPayload)
	}
	if got := gjson.GetBytes(completedPayload, "response.id").String(); got != "" {
		t.Fatalf("synthetic error completion must not provide reusable response id, got %q; payload=%s", got, completedPayload)
	}

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketSynthesizesCompletedForActionableEOF(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				serverErrCh <- errClose
			}
		}()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte, 1)
		data <- []byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"shell","arguments":"{}"}}`)
		close(data)
		errCh := make(chan *interfaces.ErrorMessage)

		base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
		h := NewOpenAIResponsesAPIHandler(base)
		timelineLog := newInMemoryWebsocketTimelineLog()
		completedOutput, _, _, errMsg, _, err := h.forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
			nil,
			nil,
		)
		if err != nil {
			serverErrCh <- err
			return
		}
		if errMsg != nil {
			serverErrCh <- fmt.Errorf("unexpected websocket error message: %v", errMsg.Error)
			return
		}
		if !strings.Contains(string(completedOutput), `"id":"fc-1"`) {
			serverErrCh <- fmt.Errorf("completed output missing output item: %s", completedOutput)
			return
		}
		if !strings.Contains(timelineLog.String(), "\"type\":\"response.completed\"") {
			serverErrCh <- errors.New("websocket timeline did not capture synthesized completed")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	if errDeadline := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); errDeadline != nil {
		t.Fatalf("set read deadline: %v", errDeadline)
	}
	_, itemPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket output item payload: %v", err)
	}
	if got := gjson.GetBytes(itemPayload, "type").String(); got != "response.output_item.done" {
		t.Fatalf("first payload type = %q, want response.output_item.done; payload=%s", got, itemPayload)
	}
	_, completedPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket completed payload: %v", err)
	}
	if got := gjson.GetBytes(completedPayload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("completed payload type = %q, want %s; payload=%s", got, wsEventTypeCompleted, completedPayload)
	}
	if got := gjson.GetBytes(completedPayload, "response.output.0.id").String(); got != "fc-1" {
		t.Fatalf("completed output item id = %q, want fc-1; payload=%s", got, completedPayload)
	}
	if got := gjson.GetBytes(completedPayload, "response.id").String(); got != "" {
		t.Fatalf("synthesized EOF completion must not create reusable response id, got %q: %s", got, completedPayload)
	}

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketEmitsErrorOnEOFAfterPartialMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				serverErrCh <- errClose
			}
		}()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte, 1)
		data <- []byte(`{"type":"response.output_item.done","item":{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"partial"}]}}`)
		close(data)
		errCh := make(chan *interfaces.ErrorMessage)

		base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
		h := NewOpenAIResponsesAPIHandler(base)
		timelineLog := newInMemoryWebsocketTimelineLog()
		completedOutput, _, _, errMsg, _, err := h.forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
			nil,
			nil,
		)
		if err != nil {
			serverErrCh <- err
			return
		}
		if errMsg == nil {
			serverErrCh <- errors.New("expected websocket error message")
			return
		}
		if !strings.Contains(errMsg.Error.Error(), "stream closed before response.completed") {
			serverErrCh <- fmt.Errorf("unexpected websocket error: %v", errMsg.Error)
			return
		}
		if !strings.Contains(string(completedOutput), `"id":"msg-1"`) {
			serverErrCh <- fmt.Errorf("completed output missing message item: %s", completedOutput)
			return
		}
		if !strings.Contains(timelineLog.String(), "stream closed before response.completed") {
			serverErrCh <- errors.New("websocket timeline did not capture stream-closed error")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	if errDeadline := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); errDeadline != nil {
		t.Fatalf("set read deadline: %v", errDeadline)
	}
	_, itemPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket output item payload: %v", err)
	}
	if got := gjson.GetBytes(itemPayload, "type").String(); got != "response.output_item.done" {
		t.Fatalf("first payload type = %q, want response.output_item.done; payload=%s", got, itemPayload)
	}
	_, errorItemPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket error output item payload: %v", err)
	}
	if got := gjson.GetBytes(errorItemPayload, "type").String(); got != "response.output_item.done" {
		t.Fatalf("second payload type = %q, want response.output_item.done; payload=%s", got, errorItemPayload)
	}
	if got := gjson.GetBytes(errorItemPayload, "item.content.0.text").String(); !strings.Contains(got, "stream closed before response.completed") {
		t.Fatalf("error item text = %q, want stream closed before response.completed; payload=%s", got, errorItemPayload)
	}
	_, completedPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket terminal error completion payload: %v", err)
	}
	if got := gjson.GetBytes(completedPayload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("completed payload type = %q, want %s; payload=%s", got, wsEventTypeCompleted, completedPayload)
	}

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketEmitsErrorOnEOFWithZeroOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				serverErrCh <- errClose
			}
		}()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte)
		close(data)
		errCh := make(chan *interfaces.ErrorMessage)

		base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
		h := NewOpenAIResponsesAPIHandler(base)
		timelineLog := newInMemoryWebsocketTimelineLog()
		completedOutput, _, _, errMsg, _, err := h.forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
			nil,
			nil,
		)
		if err != nil {
			serverErrCh <- err
			return
		}
		if errMsg == nil {
			serverErrCh <- errors.New("expected websocket error message")
			return
		}
		if responsesWebsocketOutputItemCount(completedOutput) != 0 {
			serverErrCh <- fmt.Errorf("completed output should be empty: %s", completedOutput)
			return
		}
		if !strings.Contains(timelineLog.String(), "stream closed before response.completed") {
			serverErrCh <- errors.New("websocket timeline did not capture zero-output EOF error")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	if errDeadline := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); errDeadline != nil {
		t.Fatalf("set read deadline: %v", errDeadline)
	}
	_, itemPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket output item payload: %v", err)
	}
	if got := gjson.GetBytes(itemPayload, "type").String(); got != "response.output_item.done" {
		t.Fatalf("first payload type = %q, want response.output_item.done; payload=%s", got, itemPayload)
	}
	if got := gjson.GetBytes(itemPayload, "item.content.0.text").String(); !strings.Contains(got, "stream closed before response.completed") {
		t.Fatalf("error item text = %q, want upstream EOF message; payload=%s", got, itemPayload)
	}
	_, completedPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket completed payload: %v", err)
	}
	if got := gjson.GetBytes(completedPayload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("completed payload type = %q, want %s; payload=%s", got, wsEventTypeCompleted, completedPayload)
	}
	if got := gjson.GetBytes(completedPayload, "response.metadata.error_type").String(); got != "invalid_request_error" {
		t.Fatalf("completed error type = %q, want invalid_request_error; payload=%s", got, completedPayload)
	}

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketLogsAttemptedResponseOnWriteFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte, 1)
		errCh := make(chan *interfaces.ErrorMessage)
		data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"message\",\"id\":\"out-1\"}]}}\n\n")
		close(data)
		close(errCh)

		timelineLog := newInMemoryWebsocketTimelineLog()
		if errClose := conn.Close(); errClose != nil {
			serverErrCh <- errClose
			return
		}

		_, _, _, _, _, err = (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
			nil,
			nil,
		)
		if err == nil {
			serverErrCh <- errors.New("expected websocket write failure")
			return
		}
		if !strings.Contains(timelineLog.String(), "Event: websocket.response") {
			serverErrCh <- errors.New("websocket timeline did not capture attempted downstream response")
			return
		}
		if !strings.Contains(timelineLog.String(), "\"type\":\"response.completed\"") {
			serverErrCh <- errors.New("websocket timeline did not retain attempted payload")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestResponsesWebsocketTimelineRecordsDisconnectEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	logsDir := t.TempDir()

	timelineCh := make(chan string, 1)
	router := gin.New()
	router.GET("/v1/responses/ws", func(c *gin.Context) {
		source, errSource := requestlogging.NewFileBodySourceInDir(logsDir, "websocket-timeline-test")
		if errSource != nil {
			timelineCh <- ""
			return
		}
		c.Set(requestlogging.WebsocketTimelineSourceContextKey, source)
		h.ResponsesWebsocket(c)
		timeline := ""
		if value, exists := c.Get(wsTimelineBodyKey); exists {
			if body, ok := value.([]byte); ok {
				timeline = string(body)
			}
		} else if value, exists := c.Get(requestlogging.WebsocketTimelineSourceContextKey); exists {
			if source, ok := value.(*requestlogging.FileBodySource); ok {
				body, _ := source.Bytes()
				timeline = string(body)
				_ = source.Cleanup()
			}
		}
		if value, exists := c.Get(requestlogging.APIWebsocketTimelineSourceContextKey); exists {
			if source, ok := value.(*requestlogging.FileBodySource); ok {
				_ = source.Cleanup()
			}
		}
		timelineCh <- timeline
	})

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}

	closePayload := websocket.FormatCloseMessage(websocket.CloseGoingAway, "client closing")
	if err = conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("write close control: %v", err)
	}
	_ = conn.Close()

	select {
	case timeline := <-timelineCh:
		if !strings.Contains(timeline, "Event: websocket.disconnect") {
			t.Fatalf("websocket timeline missing disconnect event: %s", timeline)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket timeline")
	}
}

func TestResponsesWebsocketClosesOnCodexUpstreamDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketUpstreamDisconnectExecutor{subscribed: make(chan string, 1)}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)

	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var sessionID string
	select {
	case sessionID = <-executor.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream disconnect subscription")
	}

	executor.TriggerDisconnect(sessionID, errors.New("upstream disconnected"))

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatalf("expected downstream websocket to close after upstream disconnect")
	}
}

func TestResponsesWebsocketCodexWebsocketPassthroughPassesCompactedRequestWithoutTranscriptMerge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketDirectCaptureExecutor{done: make(chan struct{})}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	firstRequest := []byte(`{"type":"response.create","model":"test-model","input":[{"type":"message","role":"user","content":"first"}]}`)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if errWrite := conn.WriteMessage(websocket.TextMessage, firstRequest); errWrite != nil {
		t.Fatalf("write first websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read first websocket response: %v", errRead)
	}

	compactedRequest := []byte(`{"type":"response.create","input":[{"type":"compaction_summary","summary":"compressed history"},{"type":"message","role":"user","content":"after compaction"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, compactedRequest); errWrite != nil {
		t.Fatalf("write compacted websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read compacted websocket response: %v", errRead)
	}

	select {
	case <-executor.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket passthrough")
	}

	payloads := executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("passthrough payload count = %d, want 2", len(payloads))
	}
	if got := gjson.GetBytes(payloads[0], "input").Raw; got != gjson.GetBytes(firstRequest, "input").Raw {
		t.Fatalf("first passthrough input = %s, want %s", got, gjson.GetBytes(firstRequest, "input").Raw)
	}
	if got := gjson.GetBytes(payloads[1], "input").Raw; got != gjson.GetBytes(compactedRequest, "input").Raw {
		t.Fatalf("compacted passthrough input = %s, want %s", got, gjson.GetBytes(compactedRequest, "input").Raw)
	}
	if got := gjson.GetBytes(payloads[1], "model").String(); got != "test-model" {
		t.Fatalf("compacted passthrough model = %s, want test-model", got)
	}
	if bytes.Contains(payloads[1], []byte(`"content":"first"`)) || bytes.Contains(payloads[1], []byte(`"id":"out-1"`)) {
		t.Fatalf("compacted passthrough payload contains stale transcript state: %s", payloads[1])
	}
	authIDs := executor.AuthIDs()
	if len(authIDs) != 2 || authIDs[0] != "auth-ws" || authIDs[1] != "auth-ws" {
		t.Fatalf("passthrough auth IDs = %v, want [auth-ws auth-ws]", authIDs)
	}
}

func TestResponsesWebsocketXAIWebsocketPassthroughCarriesPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	modelName := "xai-websocket-passthrough-model"
	executor := &websocketDirectCaptureExecutor{provider: "xai", done: make(chan struct{})}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-xai-ws",
		Provider:   "xai",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelName}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	firstRequest := []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-1","role":"user","content":"first"}]}`, modelName))
	if errWrite := conn.WriteMessage(websocket.TextMessage, firstRequest); errWrite != nil {
		t.Fatalf("write first websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read first websocket response: %v", errRead)
	}

	secondRequest := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-2","role":"user","content":"second"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, secondRequest); errWrite != nil {
		t.Fatalf("write second websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read second websocket response: %v", errRead)
	}

	select {
	case <-executor.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket passthrough")
	}

	payloads := executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("xai websocket payload count = %d, want 2", len(payloads))
	}
	secondPayload := payloads[1]
	if got := gjson.GetBytes(secondPayload, "type").String(); got != wsRequestTypeCreate {
		t.Fatalf("second xai passthrough type = %s, want %s: %s", got, wsRequestTypeCreate, secondPayload)
	}
	if got := gjson.GetBytes(secondPayload, "model").String(); got != modelName {
		t.Fatalf("second xai payload model = %s, want %s", got, modelName)
	}
	if got := gjson.GetBytes(secondPayload, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("second xai previous_response_id = %s, want resp-1: %s", got, secondPayload)
	}
	input := gjson.GetBytes(secondPayload, "input").Array()
	if len(input) != 1 {
		t.Fatalf("second xai passthrough input len = %d, want 1: %s", len(input), secondPayload)
	}
	if input[0].Get("id").String() != "msg-2" {
		t.Fatalf("second xai passthrough input must contain only the new turn: %s", secondPayload)
	}
	if bytes.Contains(secondPayload, []byte(`"id":"msg-1"`)) || bytes.Contains(secondPayload, []byte(`"id":"out-1"`)) {
		t.Fatalf("second xai passthrough payload contains stale transcript state: %s", secondPayload)
	}
	authIDs := executor.AuthIDs()
	if len(authIDs) != 2 || authIDs[0] != "auth-xai-ws" || authIDs[1] != "auth-xai-ws" {
		t.Fatalf("xai websocket auth IDs = %v, want [auth-xai-ws auth-xai-ws]", authIDs)
	}
}

func TestResponsesWebsocketSwitchesPinnedAuthAcrossProviders(t *testing.T) {
	for _, testCase := range []struct {
		name                      string
		xaiWebsockets             bool
		returnToDifferentXAIModel bool
	}{
		{name: "xai SSE", xaiWebsockets: false},
		{name: "xai websocket", xaiWebsockets: true},
		{name: "xai websocket different model", xaiWebsockets: true, returnToDifferentXAIModel: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			xaiModel := "xai-provider-switch-" + strings.ReplaceAll(testCase.name, " ", "-")
			returnXAIModel := xaiModel
			if testCase.returnToDifferentXAIModel {
				returnXAIModel += "-return"
			}
			codexModel := "codex-provider-switch-" + strings.ReplaceAll(testCase.name, " ", "-")
			xaiExecutor := &websocketDirectCaptureExecutor{provider: "xai"}
			codexExecutor := &websocketDirectCaptureExecutor{provider: "codex"}

			xaiAuth := &coreauth.Auth{
				ID:       "auth-" + xaiModel,
				Provider: "xai",
				Status:   coreauth.StatusActive,
			}
			if testCase.xaiWebsockets {
				xaiAuth.Attributes = map[string]string{"websockets": "true"}
			}
			codexAuth := &coreauth.Auth{
				ID:         "auth-" + codexModel,
				Provider:   "codex",
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"websockets": "true"},
			}
			selector := &orderedWebsocketSelector{order: []string{xaiAuth.ID, codexAuth.ID}}
			manager := coreauth.NewManager(nil, selector, nil)
			manager.RegisterExecutor(xaiExecutor)
			manager.RegisterExecutor(codexExecutor)
			if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
				t.Fatalf("Register xAI auth: %v", errRegister)
			}
			if _, errRegister := manager.Register(context.Background(), codexAuth); errRegister != nil {
				t.Fatalf("Register Codex auth: %v", errRegister)
			}

			registry.GetGlobalRegistry().RegisterClient(xaiAuth.ID, xaiAuth.Provider, []*registry.ModelInfo{{ID: xaiModel}})
			registry.GetGlobalRegistry().RegisterClient(codexAuth.ID, codexAuth.Provider, []*registry.ModelInfo{{ID: codexModel}})
			registeredAuthIDs := []string{xaiAuth.ID, codexAuth.ID}
			xaiAlternateAuthID := ""
			if testCase.xaiWebsockets {
				xaiAlternateAuth := &coreauth.Auth{
					ID:         "auth-alternate-" + xaiModel,
					Provider:   "xai",
					Status:     coreauth.StatusActive,
					Attributes: map[string]string{"websockets": "true"},
				}
				xaiAlternateAuthID = xaiAlternateAuth.ID
				selector.order = append(selector.order, xaiAlternateAuth.ID)
				if _, errRegister := manager.Register(context.Background(), xaiAlternateAuth); errRegister != nil {
					t.Fatalf("Register alternate xAI auth: %v", errRegister)
				}
				alternateModels := []*registry.ModelInfo{{ID: xaiModel}}
				if testCase.returnToDifferentXAIModel {
					alternateModels = []*registry.ModelInfo{{ID: returnXAIModel}}
				}
				registry.GetGlobalRegistry().RegisterClient(xaiAlternateAuth.ID, xaiAlternateAuth.Provider, alternateModels)
				registeredAuthIDs = append(registeredAuthIDs, xaiAlternateAuth.ID)
			}
			t.Cleanup(func() {
				for _, authID := range registeredAuthIDs {
					registry.GetGlobalRegistry().UnregisterClient(authID)
				}
			})

			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := NewOpenAIResponsesAPIHandler(base)
			router := gin.New()
			router.GET("/v1/responses/ws", h.ResponsesWebsocket)

			server := httptest.NewServer(router)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
			conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
			if errDial != nil {
				t.Fatalf("dial websocket: %v", errDial)
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close websocket: %v", errClose)
				}
			}()

			requests := []string{
				fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-xai-1"}]}`, xaiModel),
				fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-codex-1"}]}`, codexModel),
				fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-xai-2"}]}`, returnXAIModel),
				`{"type":"response.create","input":[{"type":"message","id":"msg-xai-3"}]}`,
			}
			for index, request := range requests {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
					t.Fatalf("write websocket message %d: %v", index+1, errWrite)
				}
				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatalf("read websocket response %d: %v", index+1, errRead)
				}
				if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
					t.Fatalf("response %d type = %s, want %s: %s", index+1, got, wsEventTypeCompleted, payload)
				}
			}

			wantReturnAuthID := xaiAuth.ID
			if testCase.returnToDifferentXAIModel {
				wantReturnAuthID = xaiAlternateAuthID
			}
			if got := xaiExecutor.AuthIDs(); len(got) != 3 || got[0] != xaiAuth.ID || got[1] != wantReturnAuthID || got[2] != wantReturnAuthID {
				t.Fatalf("xAI auth IDs = %v, want [%s %s %s]", got, xaiAuth.ID, wantReturnAuthID, wantReturnAuthID)
			}
			if got := codexExecutor.AuthIDs(); len(got) != 1 || got[0] != codexAuth.ID {
				t.Fatalf("Codex auth IDs = %v, want [%s]", got, codexAuth.ID)
			}
		})
	}
}

func TestResponsesWebsocketPinnedAuthMatchesModel(t *testing.T) {
	modelA := "xai-pinned-auth-model-a"
	modelB := "xai-pinned-auth-model-b"
	auth := &coreauth.Auth{ID: "xai-pinned-auth", Provider: "xai", Status: coreauth.StatusActive}
	otherAuthID := "xai-pinned-auth-other"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelA}})
	registry.GetGlobalRegistry().RegisterClient(otherAuthID, auth.Provider, []*registry.ModelInfo{{ID: modelB}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		registry.GetGlobalRegistry().UnregisterClient(otherAuthID)
	})

	if !responsesWebsocketPinnedAuthMatchesModel(auth, modelA, modelA, false) {
		t.Fatal("expected registered auth to match its supported model")
	}
	if responsesWebsocketPinnedAuthMatchesModel(auth, modelB, modelA, false) {
		t.Fatal("registered auth matched an unsupported model from the same provider")
	}

	disabledAuth := auth.Clone()
	disabledAuth.Disabled = true
	if responsesWebsocketPinnedAuthMatchesModel(disabledAuth, modelA, modelA, false) {
		t.Fatal("disabled auth matched a model")
	}

	cooldownAuth := auth.Clone()
	cooldownAuth.ModelStates = map[string]*coreauth.ModelState{
		modelA: {Unavailable: true, NextRetryAfter: time.Now().Add(time.Minute)},
	}
	if responsesWebsocketPinnedAuthMatchesModel(cooldownAuth, modelA, modelA, false) {
		t.Fatal("auth in model cooldown matched a model")
	}

	unregisteredAuth := &coreauth.Auth{ID: "unregistered-auth", Provider: "xai", Status: coreauth.StatusActive}
	if responsesWebsocketPinnedAuthMatchesModel(unregisteredAuth, modelA, modelA, false) {
		t.Fatal("unregistered ordinary auth matched a model")
	}
	if !responsesWebsocketPinnedAuthMatchesModel(unregisteredAuth, modelA, modelA, true) {
		t.Fatal("expected Home runtime auth to match its pinned model")
	}
	if responsesWebsocketPinnedAuthMatchesModel(unregisteredAuth, modelB, modelA, true) {
		t.Fatal("Home runtime auth matched a different model")
	}
}

func TestWebsocketUpstreamSupportsIncrementalInputForModel(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   "test-provider",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	if !h.websocketUpstreamSupportsIncrementalInputForModel("test-model") {
		t.Fatalf("expected websocket-capable upstream for test-model")
	}
}

func TestWebsocketUpstreamSupportsIncrementalInputForModelFalseWhenMixedBackends(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auths := []*coreauth.Auth{
		{
			ID:         "auth-ws",
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"websockets": "true"},
		},
		{
			ID:       "auth-http",
			Provider: "bigmodel-coding",
			Status:   coreauth.StatusActive,
		},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.3-codex"}})
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
	})
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	if h.websocketUpstreamSupportsIncrementalInputForModel("gpt-5.3-codex") {
		t.Fatalf("mixed websocket/http upstreams must replay transcript instead of preserving previous_response_id")
	}
}

func TestResponsesWebsocketAvailableAuthsForCodexTextAllowsOpenAICompat(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auths := []*coreauth.Auth{
		{
			ID:         "auth-codex",
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"websockets": "true"},
		},
		{
			ID:         "auth-compat",
			Provider:   "openai-compatible-custom-coding",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"websockets": "true"},
		},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.5"}})
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	available, modelKey := h.responsesWebsocketAvailableAuthsForModel("gpt-5.5")
	if modelKey != "gpt-5.5" {
		t.Fatalf("modelKey = %q, want gpt-5.5", modelKey)
	}
	if len(available) != 2 {
		t.Fatalf("available auths = %#v, want codex and openai-compatible", available)
	}
	if h.websocketUpstreamSupportsIncrementalInputForModel("gpt-5.5") != true {
		t.Fatalf("expected websocket capability for websocket-capable upstreams")
	}
}

func TestResponsesWebsocketAvailableAuthsForCodexImageGenerationPassthroughAllowsOpenAICompat(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auths := []*coreauth.Auth{
		{
			ID:         "auth-codex",
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"websockets": "true"},
		},
		{
			ID:         "auth-compat",
			Provider:   "openai-compatible-custom-coding",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"websockets": "true"},
		},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.5"}})
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationPassthrough}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	available, _ := h.responsesWebsocketAvailableAuthsForModel("gpt-5.5")
	if len(available) != 2 {
		t.Fatalf("available auths = %#v, want codex and openai-compatible", available)
	}
}

func TestWebsocketUpstreamSupportsCompactionReplayForModel(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "auth-codex",
		Provider: "codex",
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	if !h.websocketUpstreamSupportsCompactionReplayForModel("test-model") {
		t.Fatalf("expected codex upstream to support compaction replay")
	}
}

func TestWebsocketUpstreamSupportsCompactionReplayForModelFalseWhenMixedBackends(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auths := []*coreauth.Auth{
		{ID: "auth-codex", Provider: "codex", Status: coreauth.StatusActive},
		{ID: "auth-claude", Provider: "claude", Status: coreauth.StatusActive},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	if h.websocketUpstreamSupportsCompactionReplayForModel("test-model") {
		t.Fatalf("expected mixed backend model to disable compaction replay bypass")
	}
}

func TestResponsesWebsocketPrewarmHandledLocallyForSSEUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-sse", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		errClose := conn.Close()
		if errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","generate":false}`))
	if errWrite != nil {
		t.Fatalf("write prewarm websocket message: %v", errWrite)
	}

	_, createdPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read prewarm created message: %v", errReadMessage)
	}
	if gjson.GetBytes(createdPayload, "type").String() != "response.created" {
		t.Fatalf("created payload type = %s, want response.created", gjson.GetBytes(createdPayload, "type").String())
	}
	prewarmResponseID := gjson.GetBytes(createdPayload, "response.id").String()
	if prewarmResponseID == "" {
		t.Fatalf("prewarm response id is empty")
	}
	if executor.streamCalls != 0 {
		t.Fatalf("stream calls after prewarm = %d, want 0", executor.streamCalls)
	}

	_, completedPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read prewarm completed message: %v", errReadMessage)
	}
	if gjson.GetBytes(completedPayload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("completed payload type = %s, want %s", gjson.GetBytes(completedPayload, "type").String(), wsEventTypeCompleted)
	}
	if gjson.GetBytes(completedPayload, "response.id").String() != prewarmResponseID {
		t.Fatalf("completed response id = %s, want %s", gjson.GetBytes(completedPayload, "response.id").String(), prewarmResponseID)
	}
	if gjson.GetBytes(completedPayload, "response.usage.total_tokens").Int() != 0 {
		t.Fatalf("prewarm total tokens = %d, want 0", gjson.GetBytes(completedPayload, "response.usage.total_tokens").Int())
	}

	secondRequest := fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"type":"message","id":"msg-1"}]}`, prewarmResponseID)
	errWrite = conn.WriteMessage(websocket.TextMessage, []byte(secondRequest))
	if errWrite != nil {
		t.Fatalf("write follow-up websocket message: %v", errWrite)
	}

	_, upstreamPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read upstream completed message: %v", errReadMessage)
	}
	if gjson.GetBytes(upstreamPayload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("upstream payload type = %s, want %s", gjson.GetBytes(upstreamPayload, "type").String(), wsEventTypeCompleted)
	}
	if executor.streamCalls != 1 {
		t.Fatalf("stream calls after follow-up = %d, want 1", executor.streamCalls)
	}
	if len(executor.payloads) != 1 {
		t.Fatalf("captured upstream payloads = %d, want 1", len(executor.payloads))
	}
	forwarded := executor.payloads[0]
	if gjson.GetBytes(forwarded, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id leaked upstream: %s", forwarded)
	}
	if gjson.GetBytes(forwarded, "generate").Exists() {
		t.Fatalf("generate leaked upstream: %s", forwarded)
	}
	if gjson.GetBytes(forwarded, "model").String() != "test-model" {
		t.Fatalf("forwarded model = %s, want test-model", gjson.GetBytes(forwarded, "model").String())
	}
	input := gjson.GetBytes(forwarded, "input").Array()
	if len(input) != 1 || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected forwarded input: %s", forwarded)
	}
}

func TestResponsesWebsocketMergesTranscriptForNonPassthroughUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	if len(executor.payloads) != 2 {
		t.Fatalf("upstream payload count = %d, want 2", len(executor.payloads))
	}
	secondPayload := executor.payloads[1]
	if gjson.GetBytes(secondPayload, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not be sent on non-passthrough upstream: %s", secondPayload)
	}
	input := gjson.GetBytes(secondPayload, "input").Array()
	if len(input) != 3 {
		t.Fatalf("second upstream input len = %d, want 3: %s", len(input), secondPayload)
	}
	if input[0].Get("id").String() != "msg-1" || input[1].Get("id").String() != "out-1" || input[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected merged upstream input: %s", secondPayload)
	}
}

func TestResponsesWebsocketDoesNotInjectPreviousResponseIDWhenPendingToolOutputMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCompactionCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"message","role":"user","id":"summary-1","content":"compacted summary"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	executor.mu.Lock()
	payloads := append([][]byte(nil), executor.streamPayloads...)
	executor.mu.Unlock()

	if len(payloads) != 2 {
		t.Fatalf("upstream payload count = %d, want 2", len(payloads))
	}
	secondPayload := payloads[1]
	if gjson.GetBytes(secondPayload, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not be injected when pending tool output is missing: %s", secondPayload)
	}
	input := gjson.GetBytes(secondPayload, "input").Array()
	if len(input) != 3 {
		t.Fatalf("second upstream input len = %d, want 3: %s", len(input), secondPayload)
	}
	if input[0].Get("id").String() != "msg-1" || input[1].Get("id").String() != "fc-1" || input[2].Get("id").String() != "summary-1" {
		t.Fatalf("unexpected merged upstream input when pending tool output is missing: %s", secondPayload)
	}
}

func TestResponsesWebsocketStripsGenerateWhenWebsocketAttemptFallsBackToHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-ws", "auth-http"}}
	executor := &websocketBootstrapFallbackExecutor{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	authWS := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authWS); err != nil {
		t.Fatalf("Register websocket auth: %v", err)
	}
	authHTTP := &coreauth.Auth{ID: "auth-http", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), authHTTP); err != nil {
		t.Fatalf("Register HTTP auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(authWS.ID, authWS.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(authHTTP.ID, authHTTP.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authWS.ID)
		registry.GetGlobalRegistry().UnregisterClient(authHTTP.ID)
	})
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 1},
	}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	request := `{"type":"response.create","model":"test-model","generate":true,"input":[{"type":"message","id":"msg-1"}]}`
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("payload type = %s, want %s: %s", got, wsEventTypeCompleted, payload)
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-ws" || got[1] != "auth-http" {
		t.Fatalf("selected auth IDs = %v, want [auth-ws auth-http]", got)
	}

	wsPayloads := executor.Payloads("auth-ws")
	if len(wsPayloads) != 1 {
		t.Fatalf("auth-ws payload count = %d, want 1", len(wsPayloads))
	}
	if !gjson.GetBytes(wsPayloads[0], "generate").Exists() {
		t.Fatalf("websocket attempt payload unexpectedly stripped generate: %s", wsPayloads[0])
	}

	httpPayloads := executor.Payloads("auth-http")
	if len(httpPayloads) != 1 {
		t.Fatalf("auth-http payload count = %d, want 1", len(httpPayloads))
	}
	if gjson.GetBytes(httpPayloads[0], "generate").Exists() {
		t.Fatalf("generate leaked after HTTP fallback: %s", httpPayloads[0])
	}
}

func TestWebsocketClientAddressUsesGinClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, engine := gin.CreateTestContext(recorder)
	if err := engine.SetTrustedProxies([]string{"0.0.0.0/0", "::/0"}); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/ws", nil)
	req.RemoteAddr = "172.18.0.1:34282"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	c.Request = req

	if got := websocketClientAddress(c); got != strings.TrimSpace(c.ClientIP()) {
		t.Fatalf("websocketClientAddress = %q, ClientIP = %q", got, c.ClientIP())
	}
}

func TestWebsocketClientAddressReturnsEmptyForNilContext(t *testing.T) {
	if got := websocketClientAddress(nil); got != "" {
		t.Fatalf("websocketClientAddress(nil) = %q, want empty", got)
	}
}

func TestResponsesWebsocketPinsOnlyWebsocketCapableAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-sse", "auth-ws"}}
	executor := &websocketAuthCaptureExecutor{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	authSSE := &coreauth.Auth{ID: "auth-sse", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), authSSE); err != nil {
		t.Fatalf("Register SSE auth: %v", err)
	}
	authWS := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authWS); err != nil {
		t.Fatalf("Register websocket auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(authSSE.ID, authSSE.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(authWS.ID, authWS.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authSSE.ID)
		registry.GetGlobalRegistry().UnregisterClient(authWS.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-sse" || got[1] != "auth-ws" {
		t.Fatalf("selected auth IDs = %v, want [auth-sse auth-ws]", got)
	}
}

func TestResponsesWebsocketReleasesPinnedAuthAfterQuotaError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &websocketPinnedFailoverExecutor{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	authA := &coreauth.Auth{
		ID:         "auth-a",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authA); err != nil {
		t.Fatalf("Register auth A: %v", err)
	}
	authB := &coreauth.Auth{
		ID:         "auth-b",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authB); err != nil {
		t.Fatalf("Register auth B: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(authA.ID, authA.Provider, []*registry.ModelInfo{{ID: "quota-model"}})
	registry.GetGlobalRegistry().RegisterClient(authB.ID, authB.Provider, []*registry.ModelInfo{{ID: "quota-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA.ID)
		registry.GetGlobalRegistry().UnregisterClient(authB.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"quota-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","previous_response_id":"resp_auth_a_1","input":[{"type":"message","id":"msg-2"}]}`,
		`{"type":"response.create","previous_response_id":"resp_auth_a_1","input":[{"type":"message","id":"msg-3"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		wantType := wsEventTypeCompleted
		if i == 1 {
			wantType = "response.output_item.done"
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wantType {
			t.Fatalf("message %d payload type = %s, want %s: %s", i+1, got, wantType, payload)
		}
		if i == 1 && !strings.Contains(gjson.GetBytes(payload, "item.content.0.text").String(), "rate_limit_exceeded") {
			t.Fatalf("quota output item must mention rate_limit_exceeded: %s", payload)
		}
		if i == 1 {
			_, completedPayload, errReadCompleted := conn.ReadMessage()
			if errReadCompleted != nil {
				t.Fatalf("read websocket completed message %d: %v", i+1, errReadCompleted)
			}
			if got := gjson.GetBytes(completedPayload, "type").String(); got != wsEventTypeCompleted {
				t.Fatalf("quota completed payload type = %s, want %s: %s", got, wsEventTypeCompleted, completedPayload)
			}
			if got := gjson.GetBytes(completedPayload, "response.id").String(); got != "" {
				t.Fatalf("quota synthetic completion must not provide reusable response id, got %q: %s", got, completedPayload)
			}
		}
	}

	if got := executor.AuthIDs(); len(got) != 3 || got[0] != "auth-a" || got[1] != "auth-a" || got[2] != "auth-b" {
		t.Fatalf("selected auth IDs = %v, want [auth-a auth-a auth-b]", got)
	}

	authBPayloads := executor.Payloads("auth-b")
	if len(authBPayloads) != 1 {
		t.Fatalf("auth-b payload count = %d, want 1", len(authBPayloads))
	}
	authBPayload := authBPayloads[0]
	if gjson.GetBytes(authBPayload, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id leaked after auth failover: %s", authBPayload)
	}
	authBInput := gjson.GetBytes(authBPayload, "input").Raw
	if !strings.Contains(authBInput, `"id":"msg-1"`) || !strings.Contains(authBInput, `"id":"msg-3"`) {
		t.Fatalf("auth-b replay input missing expected transcript items: %s", authBInput)
	}
}

func TestResponsesWebsocketRepinsAfterEgressUnboundFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &websocketPinnedFailoverExecutor{secondAuthAError: internalegress.RuntimeError(internalegress.ErrEgressUnbound)}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)
	for _, auth := range []*coreauth.Auth{
		{ID: "auth-a", Provider: executor.Identifier(), Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}},
		{ID: "auth-b", Provider: executor.Identifier(), Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "egress-switch-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	for turn := 1; turn <= 3; turn++ {
		request := fmt.Sprintf(`{"type":"response.create","model":"egress-switch-model","input":[{"type":"message","id":"msg-%d"}]}`, turn)
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", turn, errWrite)
		}
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("read websocket message %d: %v", turn, errRead)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d type = %s, want %s: %s", turn, got, wsEventTypeCompleted, payload)
		}
	}

	if got := executor.AuthIDs(); len(got) != 4 || got[0] != "auth-a" || got[1] != "auth-a" || got[2] != "auth-b" || got[3] != "auth-b" {
		t.Fatalf("selected auth IDs = %v, want [auth-a auth-a auth-b auth-b]", got)
	}
}

func TestResponsesWebsocketRetriesPreviousResponseNotFoundWithTranscriptReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketPreviousResponseReplayExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:         "auth-replay",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "replay-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"replay-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","previous_response_id":"resp_missing","input":[{"type":"message","id":"msg-2"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s: %s", i+1, got, wsEventTypeCompleted, payload)
		}
	}

	payloads := executor.Payloads()
	if len(payloads) != 3 {
		t.Fatalf("upstream payload count = %d, want 3", len(payloads))
	}
	if got := gjson.GetBytes(payloads[1], "previous_response_id").String(); got != "resp_missing" {
		t.Fatalf("second payload previous_response_id = %q, want resp_missing: %s", got, payloads[1])
	}
	if gjson.GetBytes(payloads[2], "previous_response_id").Exists() {
		t.Fatalf("replay payload must drop previous_response_id: %s", payloads[2])
	}
	replayInput := gjson.GetBytes(payloads[2], "input").Raw
	if !strings.Contains(replayInput, `"id":"msg-1"`) || !strings.Contains(replayInput, `"id":"msg-2"`) {
		t.Fatalf("replay input missing expected transcript items: %s", replayInput)
	}
}

// TestResponsesWebsocketReplayPreservesProjectContextAcrossIncrementalTurns 复现客户反馈：
// 在 codex passthrough 模式下客户端用 previous_response_id 发送增量 input。
// 若某一轮上游报 "No tool call found" 触发 transcript replay，replay 必须重发完整 transcript
// （含早期 user message / 项目上下文 / 历史 function_call），否则上游会"忘记"项目上下文。
func TestResponsesWebsocketReplayPreservesProjectContextAcrossIncrementalTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketPreviousResponseReplayExecutor{
		provider:     "codex",
		failOnCall:   3,
		errorMessage: `{"error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"No tool call found for custom tool call output with call_id call_2","param":"previous_response_id"}}`,
		outputByCall: map[int]string{
			1: `[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch","input":"*** Begin Patch\npatch-1\n*** End Patch"}]`,
			2: `[{"type":"custom_tool_call","id":"ctc-2","call_id":"call-2","name":"apply_patch","input":"*** Begin Patch\npatch-2\n*** End Patch"}]`,
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:         "auth-codex-incremental-replay",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "codex-incremental-replay-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	// 模拟 Codex CLI 多轮工具调用：
	// 第 1 轮：客户端发送完整 input（含项目上下文 user message）
	// 第 2 轮：incremental，previous_response_id=resp_replay_1，input=[call-1 输出]
	// 第 3 轮：incremental，previous_response_id=resp_replay_2，input=[call-2 输出]，上游报错触发 replay
	// 第 4 轮：服务端 replay，客户端收到 response.completed
	requests := []string{
		`{"type":"response.create","model":"codex-incremental-replay-model","instructions":"you are a coding agent","input":[{"type":"message","id":"msg-project-context","role":"user","content":[{"type":"input_text","text":"Project context: repo=CliRelay, working dir=/Volumes/Data/workspace/CliRelay"}]}]}`,
		`{"type":"response.create","previous_response_id":"resp_replay_1","input":[{"type":"custom_tool_call_output","id":"tool-out-1","call_id":"call-1","output":"patch-1 applied"}]}`,
		`{"type":"response.create","previous_response_id":"resp_replay_2","input":[{"type":"custom_tool_call_output","id":"tool-out-2","call_id":"call-2","output":"patch-2 applied"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, requests[i])
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s: %s", i+1, got, wsEventTypeCompleted, payload)
		}
	}

	payloads := executor.Payloads()
	if len(payloads) != 4 {
		t.Fatalf("upstream payload count = %d, want 4 (3 client turns + 1 replay): %v", len(payloads), payloads)
	}

	t.Logf("DEBUG payloads[0] (turn 1, full input): %s", payloads[0])
	t.Logf("DEBUG payloads[1] (turn 2, incremental): %s", payloads[1])
	t.Logf("DEBUG payloads[2] (turn 3, incremental, fails): %s", payloads[2])
	t.Logf("DEBUG payloads[3] (turn 4, replay): %s", payloads[3])

	// 前两轮是 incremental（带 previous_response_id）
	if got := gjson.GetBytes(payloads[1], "previous_response_id").String(); got != "resp_replay_1" {
		t.Fatalf("second payload previous_response_id = %q, want resp_replay_1: %s", got, payloads[1])
	}
	if got := gjson.GetBytes(payloads[2], "previous_response_id").String(); got != "resp_replay_2" {
		t.Fatalf("third payload previous_response_id = %q, want resp_replay_2: %s", got, payloads[2])
	}

	// 第 4 个 payload 是 replay，应该剥离 previous_response_id
	if gjson.GetBytes(payloads[3], "previous_response_id").Exists() {
		t.Fatalf("replay payload must drop previous_response_id: %s", payloads[3])
	}

	replayInput := gjson.GetBytes(payloads[3], "input").Raw
	// 必须保留最初的项目上下文 user message
	if !strings.Contains(replayInput, `"id":"msg-project-context"`) {
		t.Fatalf("replay input missing project context user message: %s", replayInput)
	}
	// 必须保留第一轮的 function_call (call-1)
	if !strings.Contains(replayInput, `"call_id":"call-1"`) || !strings.Contains(replayInput, `"name":"apply_patch"`) {
		t.Fatalf("replay input missing first turn function_call (call-1): %s", replayInput)
	}
	// 必须保留第二轮的 tool_call_output (call-1 的输出)
	if !strings.Contains(replayInput, `"id":"tool-out-1"`) || !strings.Contains(replayInput, `"patch-1 applied"`) {
		t.Fatalf("replay input missing second turn tool_call_output (tool-out-1): %s", replayInput)
	}
	// 必须保留第二轮的 function_call (call-2)
	if !strings.Contains(replayInput, `"call_id":"call-2"`) {
		t.Fatalf("replay input missing second turn function_call (call-2): %s", replayInput)
	}
	// 必须保留第三轮的 tool_call_output (call-2 的输出)
	if !strings.Contains(replayInput, `"id":"tool-out-2"`) || !strings.Contains(replayInput, `"patch-2 applied"`) {
		t.Fatalf("replay input missing third turn tool_call_output (tool-out-2): %s", replayInput)
	}
}

// TestResponsesWebsocketAstronReplaysParallelToolTranscript verifies that an
// Astron-backed websocket session never relies on upstream previous_response_id
// state after the provider has been selected. The next turn must carry the
// complete user/tool transcript so all parallel tool results remain associated
// with their original calls.
func TestResponsesWebsocketAstronReplaysParallelToolTranscript(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketPreviousResponseReplayExecutor{
		provider:    internalconfig.DefaultAstronCodeProviderName,
		failOnCall:  99,
		firstOutput: `[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"read_file","arguments":"{\"path\":\"a.go\"}"},{"type":"function_call","id":"fc-2","call_id":"call-2","name":"read_file","arguments":"{\"path\":\"b.go\"}"},{"type":"function_call","id":"fc-3","call_id":"call-3","name":"read_file","arguments":"{\"path\":\"c.go\"}"}]`,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "auth-astron-full-replay",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	const model = "astron-full-replay-model"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"astron-full-replay-model","input":[{"type":"message","id":"msg-original","role":"user","content":[{"type":"input_text","text":"inspect all three files, then explain the bug"}]}]}`,
		`{"type":"response.create","previous_response_id":"resp_replay_1","input":[
			{"type":"function_call_output","id":"tool-out-1","call_id":"call-1","output":"a.go result"},
			{"type":"function_call_output","id":"tool-out-2","call_id":"call-2","output":"b.go result"},
			{"type":"function_call_output","id":"tool-out-3","call_id":"call-3","output":"c.go result"}
		]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s: %s", i+1, got, wsEventTypeCompleted, payload)
		}
	}

	payloads := executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("upstream payload count = %d, want 2: %v", len(payloads), payloads)
	}
	secondPayload := payloads[1]
	if gjson.GetBytes(secondPayload, "previous_response_id").Exists() {
		t.Fatalf("Astron replay payload must drop previous_response_id: %s", secondPayload)
	}

	replayInput := gjson.GetBytes(secondPayload, "input").Array()
	if len(replayInput) != 7 {
		t.Fatalf("Astron replay input len = %d, want 7: %s", len(replayInput), secondPayload)
	}
	if replayInput[0].Get("id").String() != "msg-original" {
		t.Fatalf("Astron replay lost the original user message: %s", secondPayload)
	}
	for i := 1; i <= 3; i++ {
		callID := fmt.Sprintf("call-%d", i)
		call := replayInput[i]
		output := replayInput[i+3]
		if call.Get("type").String() != "function_call" || call.Get("call_id").String() != callID {
			t.Fatalf("Astron replay tool call %d is missing or reordered: %s", i, secondPayload)
		}
		if output.Get("type").String() != "function_call_output" || output.Get("call_id").String() != callID {
			t.Fatalf("Astron replay tool output %d is missing or mismatched: %s", i, secondPayload)
		}
	}
}

func TestResponsesWebsocketRetriesZhipuMessagesErrorWithTranscriptReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketPreviousResponseReplayExecutor{
		provider:     "bigmodel-coding",
		errorMessage: `{"error":{"code":"1214","message":"messages 参数非法。请检查文档。"}}`,
		firstOutput:  `[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch","input":"*** Begin Patch"}]`,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "auth-zhipu-replay",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "glm-5.2"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"glm-5.2","input":[{"type":"message","id":"msg-1","role":"user","content":[{"type":"input_text","text":"apply the patch"}]}]}`,
		`{"type":"response.create","previous_response_id":"resp_replay_1","input":[{"type":"custom_tool_call_output","id":"tool-out-1","call_id":"call-1","output":"done"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s: %s", i+1, got, wsEventTypeCompleted, payload)
		}
	}

	payloads := executor.Payloads()
	if len(payloads) != 3 {
		t.Fatalf("upstream payload count = %d, want 3", len(payloads))
	}
	if got := gjson.GetBytes(payloads[1], "previous_response_id").String(); got != "resp_replay_1" {
		t.Fatalf("incremental payload previous_response_id = %q, want resp_replay_1: %s", got, payloads[1])
	}
	if gjson.GetBytes(payloads[2], "previous_response_id").Exists() {
		t.Fatalf("replay payload must drop previous_response_id: %s", payloads[2])
	}
	replayInput := gjson.GetBytes(payloads[2], "input").Array()
	if len(replayInput) != 3 {
		t.Fatalf("replay input len = %d, want 3: %s", len(replayInput), payloads[2])
	}
	if replayInput[0].Get("id").String() != "msg-1" ||
		replayInput[1].Get("id").String() != "ctc-1" ||
		replayInput[1].Get("call_id").String() != "call-1" ||
		replayInput[2].Get("id").String() != "tool-out-1" ||
		replayInput[2].Get("call_id").String() != "call-1" {
		t.Fatalf("replay input missing paired tool transcript: %s", payloads[2])
	}
}

func TestResponsesWebsocketRetriesZeroOutputEOFWithTranscriptReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketZeroOutputEOFReplayExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:         "auth-eof-retry",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "eof-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"eof-model","input":[{"type":"message","id":"msg-1"}]}`)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("payload type = %s, want %s: %s", got, wsEventTypeCompleted, payload)
	}
	if got := gjson.GetBytes(payload, "response.metadata.error_code").String(); got != "" {
		t.Fatalf("zero-output EOF should be hidden by replay, got error_code %q: %s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.id").String(); got != "resp_eof_retry_2" {
		t.Fatalf("response.id = %q, want retry response id; payload=%s", got, payload)
	}

	payloads := executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("upstream payload count = %d, want 2", len(payloads))
	}
	if got := gjson.GetBytes(payloads[0], "input.0.id").String(); got != "msg-1" {
		t.Fatalf("first payload input id = %q, want msg-1: %s", got, payloads[0])
	}
	if got := gjson.GetBytes(payloads[1], "input.0.id").String(); got != "msg-1" {
		t.Fatalf("retry payload input id = %q, want msg-1: %s", got, payloads[1])
	}
}

func TestResponsesWebsocketRetriesLifecycleOnlyEOFWithTranscriptReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketZeroOutputEOFReplayExecutor{firstCallLifecyclePayloads: true}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:         "auth-lifecycle-eof-retry",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "lifecycle-eof-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"lifecycle-eof-model","input":[{"type":"message","id":"msg-1"}]}`)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("first visible payload type = %s, want %s from replay: %s", got, wsEventTypeCompleted, payload)
	}
	if got := gjson.GetBytes(payload, "response.metadata.error_code").String(); got != "" {
		t.Fatalf("lifecycle-only EOF should be hidden by replay, got error_code %q: %s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.id").String(); got != "resp_eof_retry_2" {
		t.Fatalf("response.id = %q, want retry response id; payload=%s", got, payload)
	}

	if payloads := executor.Payloads(); len(payloads) != 2 {
		t.Fatalf("upstream payload count = %d, want 2", len(payloads))
	}
}

func TestResponsesWebsocketRetriesLifecycleOnlyEOFTwiceWithTranscriptReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketZeroOutputEOFReplayExecutor{failCalls: 2, firstCallLifecyclePayloads: true}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:         "auth-lifecycle-eof-retry-twice",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "lifecycle-eof-twice-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"lifecycle-eof-twice-model","input":[{"type":"message","id":"msg-1"}]}`)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("first visible payload type = %s, want %s from second replay: %s", got, wsEventTypeCompleted, payload)
	}
	if got := gjson.GetBytes(payload, "response.metadata.error_code").String(); got != "" {
		t.Fatalf("repeated lifecycle-only EOF should be hidden by replay, got error_code %q: %s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.id").String(); got != "resp_eof_retry_3" {
		t.Fatalf("response.id = %q, want second retry response id; payload=%s", got, payload)
	}

	if payloads := executor.Payloads(); len(payloads) != 3 {
		t.Fatalf("upstream payload count = %d, want 3", len(payloads))
	}
}

func TestResponsesWebsocketRetriesLifecycleOnlyEOFWithConfiguredReplayLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const replayRetries = 8
	executor := &websocketZeroOutputEOFReplayExecutor{failCalls: replayRetries, firstCallLifecyclePayloads: true}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:         "auth-lifecycle-eof-retry-eight",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "lifecycle-eof-eight-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{ResponsesWebsocketReplayRetries: intPointer(replayRetries)},
	}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"lifecycle-eof-eight-model","input":[{"type":"message","id":"msg-1"}]}`)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("first visible payload type = %s, want %s after configured replays: %s", got, wsEventTypeCompleted, payload)
	}
	if got := gjson.GetBytes(payload, "response.metadata.error_code").String(); got != "" {
		t.Fatalf("configured lifecycle-only EOF replays should hide error, got error_code %q: %s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.id").String(); got != "resp_eof_retry_9" {
		t.Fatalf("response.id = %q, want ninth attempt response id; payload=%s", got, payload)
	}

	if payloads := executor.Payloads(); len(payloads) != replayRetries+1 {
		t.Fatalf("upstream payload count = %d, want %d", len(payloads), replayRetries+1)
	}
}

func TestResponsesWebsocketRetriesLifecycleTransientErrorWithConfiguredReplayLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const replayRetries = 8
	executor := &websocketZeroOutputEOFReplayExecutor{
		failCalls:                  replayRetries,
		firstCallLifecyclePayloads: true,
		failPayload:                xunfeiBusyWebsocketErrorPayload(),
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:         "auth-lifecycle-transient-error-retry-eight",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "lifecycle-transient-error-eight-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{ResponsesWebsocketReplayRetries: intPointer(replayRetries)},
	}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"lifecycle-transient-error-eight-model","input":[{"type":"message","id":"msg-1"}]}`)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("first visible payload type = %s, want %s after configured transient error replays: %s", got, wsEventTypeCompleted, payload)
	}
	if got := gjson.GetBytes(payload, "response.metadata.error_code").String(); got != "" {
		t.Fatalf("configured transient error replays should hide error, got error_code %q: %s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.id").String(); got != "resp_eof_retry_9" {
		t.Fatalf("response.id = %q, want ninth attempt response id; payload=%s", got, payload)
	}

	if payloads := executor.Payloads(); len(payloads) != replayRetries+1 {
		t.Fatalf("upstream payload count = %d, want %d", len(payloads), replayRetries+1)
	}
}

func TestResponsesWebsocketRetriesTransportDecodeErrorOnAnotherAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &websocketTransientTransportFailoverExecutor{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	for _, authID := range []string{"auth-a", "auth-b"} {
		auth := &coreauth.Auth{
			ID:         authID,
			Provider:   executor.Identifier(),
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"websockets": "true"},
		}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register %s: %v", authID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "transport-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{ResponsesWebsocketReplayRetries: intPointer(1)},
	}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"transport-model","input":[{"type":"message","id":"msg-1"}]}`)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("payload type = %s, want %s after replay: %s", got, wsEventTypeCompleted, payload)
	}
	if got := gjson.GetBytes(payload, "response.id").String(); got != "resp-auth-b" {
		t.Fatalf("response.id = %q, want auth-b replay response: %s", got, payload)
	}
	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-a" || got[1] != "auth-b" {
		t.Fatalf("selected auth IDs = %v, want [auth-a auth-b]", got)
	}
}

func TestResponsesWebsocketConfiguredReplayLimitZeroDisablesLifecycleEOFReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketZeroOutputEOFReplayExecutor{failCalls: 1, firstCallLifecyclePayloads: true}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:         "auth-lifecycle-eof-retry-disabled",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "lifecycle-eof-disabled-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{ResponsesWebsocketReplayRetries: intPointer(0)},
	}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"lifecycle-eof-disabled-model","input":[{"type":"message","id":"msg-1"}]}`)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}

	var completedPayload []byte
	for i := 0; i < 4; i++ {
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if gjson.GetBytes(payload, "type").String() == wsEventTypeCompleted {
			completedPayload = payload
			break
		}
	}
	if len(completedPayload) == 0 {
		t.Fatal("did not receive terminal completed payload")
	}
	if got := gjson.GetBytes(completedPayload, "response.metadata.error_code").String(); got != "request_timeout" {
		t.Fatalf("error_code = %q, want request_timeout when replay disabled: %s", got, completedPayload)
	}
	if payloads := executor.Payloads(); len(payloads) != 1 {
		t.Fatalf("upstream payload count = %d, want 1 when replay disabled", len(payloads))
	}
}

func TestResponsesWebsocketConfiguredReplayLimitZeroDisablesTransientErrorReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketZeroOutputEOFReplayExecutor{
		failCalls:                  1,
		firstCallLifecyclePayloads: true,
		failPayload:                xunfeiBusyWebsocketErrorPayload(),
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:         "auth-lifecycle-transient-error-retry-disabled",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "lifecycle-transient-error-disabled-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{ResponsesWebsocketReplayRetries: intPointer(0)},
	}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"lifecycle-transient-error-disabled-model","input":[{"type":"message","id":"msg-1"}]}`)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}

	var completedPayload []byte
	sawErrorEvent := false
	for i := 0; i < 6; i++ {
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		switch gjson.GetBytes(payload, "type").String() {
		case wsEventTypeError:
			sawErrorEvent = true
		case wsEventTypeCompleted:
			completedPayload = payload
		}
		if len(completedPayload) > 0 {
			break
		}
	}
	if !sawErrorEvent {
		t.Fatal("expected transient upstream error event to be visible when replay is disabled")
	}
	if len(completedPayload) == 0 {
		t.Fatal("did not receive terminal completed payload")
	}
	if got := gjson.GetBytes(completedPayload, "response.metadata.error_code").String(); got != "request_timeout" {
		t.Fatalf("error_code = %q, want request_timeout when transient replay disabled: %s", got, completedPayload)
	}
	if payloads := executor.Payloads(); len(payloads) != 1 {
		t.Fatalf("upstream payload count = %d, want 1 when replay disabled", len(payloads))
	}
}

func TestShouldReleaseResponsesWebsocketPinnedAuth(t *testing.T) {
	cases := []struct {
		name string
		err  *interfaces.ErrorMessage
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "request timeout", err: &interfaces.ErrorMessage{StatusCode: http.StatusRequestTimeout, Error: fmt.Errorf("stream closed before response.completed")}, want: true},
		{name: "service unavailable", err: &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: fmt.Errorf("websocket bootstrap failed")}, want: true},
		{name: "bad request", err: &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("invalid request")}, want: false},
		{name: "previous response missing", err: &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("previous_response_not_found")}, want: true},
		{name: "empty stream", err: &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: fmt.Errorf("empty_stream: upstream stream closed before first payload")}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReleaseResponsesWebsocketPinnedAuth(tc.err); got != tc.want {
				t.Fatalf("shouldReleaseResponsesWebsocketPinnedAuth() = %v, want %v", got, tc.want)
			}
		})
	}
}

type websocketPinnedPrematureCloseExecutor struct {
	mu       sync.Mutex
	authIDs  []string
	calls    map[string]int
	payloads map[string][][]byte
}

func (e *websocketPinnedPrematureCloseExecutor) Identifier() string { return "test-provider" }

func (e *websocketPinnedPrematureCloseExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPinnedPrematureCloseExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}

	e.mu.Lock()
	if e.calls == nil {
		e.calls = make(map[string]int)
	}
	if e.payloads == nil {
		e.payloads = make(map[string][][]byte)
	}
	e.authIDs = append(e.authIDs, authID)
	e.calls[authID]++
	call := e.calls[authID]
	e.payloads[authID] = append(e.payloads[authID], bytes.Clone(req.Payload))
	e.mu.Unlock()

	if authID == "auth-a" && call == 2 {
		chunks := make(chan coreexecutor.StreamChunk, 1)
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.output_item.added","item":{"id":"partial-1","type":"message"}}`)}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}

	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%s-%d","output":[{"type":"message","id":"out-%s-%d"}]}}`, authID, call, authID, call))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketPinnedPrematureCloseExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketPinnedPrematureCloseExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPinnedPrematureCloseExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketPinnedPrematureCloseExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *websocketPinnedPrematureCloseExecutor) Payloads(authID string) [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	src := e.payloads[authID]
	out := make([][]byte, len(src))
	for i := range src {
		out[i] = bytes.Clone(src[i])
	}
	return out
}

func TestResponsesWebsocketReleasesPinnedAuthAfterStreamClosed408(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &websocketPinnedPrematureCloseExecutor{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	authA := &coreauth.Auth{
		ID:         "auth-a",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authA); err != nil {
		t.Fatalf("Register auth A: %v", err)
	}
	authB := &coreauth.Auth{
		ID:         "auth-b",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authB); err != nil {
		t.Fatalf("Register auth B: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(authA.ID, authA.Provider, []*registry.ModelInfo{{ID: "stream-model"}})
	registry.GetGlobalRegistry().RegisterClient(authB.ID, authB.Provider, []*registry.ModelInfo{{ID: "stream-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA.ID)
		registry.GetGlobalRegistry().UnregisterClient(authB.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"stream-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","previous_response_id":"resp-auth-a-1","input":[{"type":"message","id":"msg-2"}]}`,
		`{"type":"response.create","previous_response_id":"resp-auth-a-1","input":[{"type":"message","id":"msg-3"}]}`,
	}
	wantTypes := []string{wsEventTypeCompleted, wsEventTypeCompleted, wsEventTypeCompleted}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		if i == 1 {
			for {
				_, payload, errReadMessage := conn.ReadMessage()
				if errReadMessage != nil {
					t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
				}
				got := gjson.GetBytes(payload, "type").String()
				if got != wsEventTypeCompleted {
					continue
				}
				if gotCode := gjson.GetBytes(payload, "response.metadata.error_code").String(); gotCode != "request_timeout" {
					t.Fatalf("stream-closed payload error_code = %q, want request_timeout: %s", gotCode, payload)
				}
				break
			}
			continue
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wantTypes[i] {
			t.Fatalf("message %d payload type = %s, want %s: %s", i+1, got, wantTypes[i], payload)
		}
	}

	authIDs := executor.AuthIDs()
	if len(authIDs) != 3 || authIDs[0] != "auth-a" || authIDs[1] != "auth-a" {
		t.Fatalf("selected auth IDs = %v, want auth-a for first two turns", authIDs)
	}

	replayAuthID := authIDs[2]
	replayPayloads := executor.Payloads(replayAuthID)
	if len(replayPayloads) == 0 {
		t.Fatalf("replay auth %s has no payloads", replayAuthID)
	}
	replayPayload := replayPayloads[len(replayPayloads)-1]
	if gjson.GetBytes(replayPayload, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id leaked after stream-closed replay: %s", replayPayload)
	}
	replayInput := gjson.GetBytes(replayPayload, "input").Raw
	if !strings.Contains(replayInput, `"id":"msg-1"`) || !strings.Contains(replayInput, `"id":"msg-3"`) {
		t.Fatalf("replay input missing expected transcript items: %s", replayInput)
	}
}

func TestNormalizeResponsesWebsocketRequestTreatsTranscriptReplacementAsReset(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"},{"type":"function_call","id":"fc-1","call_id":"call-1"},{"type":"function_call_output","id":"tool-out-1","call_id":"call-1"},{"type":"message","id":"assistant-1","role":"assistant"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call","id":"fc-compact","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-2"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not exist in transcript replacement mode")
	}
	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 2 {
		t.Fatalf("replacement input len = %d, want 2: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "fc-compact" || items[1].Get("id").String() != "msg-2" {
		t.Fatalf("replacement transcript was not preserved as-is: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match replacement request")
	}
}

func TestNormalizeResponsesWebsocketRequestDoesNotTreatDeveloperMessageAsReplacement(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","id":"dev-1","role":"developer"},{"type":"message","id":"msg-2"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 4 {
		t.Fatalf("merged input len = %d, want 4: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "msg-1" ||
		items[1].Get("id").String() != "assistant-1" ||
		items[2].Get("id").String() != "dev-1" ||
		items[3].Get("id").String() != "msg-2" {
		t.Fatalf("developer follow-up should preserve merge behavior: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match merged request")
	}
}

func TestNormalizeResponsesWebsocketRequestDropsDuplicateFunctionCallsByCallID(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"function_call","id":"fc-1","call_id":"call-1"},{"type":"function_call_output","id":"tool-out-1","call_id":"call-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "fc-1" ||
		items[1].Get("id").String() != "tool-out-1" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected merged input order: %s", normalized)
	}
}

func TestNormalizeResponsesWebsocketRequestDropsDuplicateInputItemsByID(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1","role":"user"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"}
	]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"function_call","id":"fc-1","call_id":"call-2","name":"tool"},{"type":"function_call_output","id":"tool-out-1","call_id":"call-2"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, true)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "msg-1" ||
		items[1].Get("id").String() != "fc-1" ||
		items[1].Get("call_id").String() != "call-2" ||
		items[2].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged input order: %s", normalized)
	}
}

func TestNormalizeResponsesWebsocketRequestTreatsCustomToolTranscriptReplacementAsReset(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"},{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-1","call_id":"call-1"},{"type":"message","id":"assistant-1","role":"assistant"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"custom_tool_call","id":"ctc-compact","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-compact","call_id":"call-1"},{"type":"message","id":"msg-2"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not exist in transcript replacement mode")
	}
	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("replacement input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "ctc-compact" ||
		items[1].Get("id").String() != "tool-out-compact" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("replacement transcript was not preserved as-is: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match replacement request")
	}
}

func TestNormalizeResponsesWebsocketRequestDropsDuplicateCustomToolCallsByCallID(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-1","call_id":"call-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "ctc-1" ||
		items[1].Get("id").String() != "tool-out-1" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected merged input order: %s", normalized)
	}
}

func TestDedupeResponsesWebsocketInputItemsByIDAfterRepair(t *testing.T) {
	payload := []byte(`{"input":[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"tool"},{"type":"custom_tool_call","id":"ctc-1","call_id":"call-2","name":"tool"},{"type":"custom_tool_call_output","id":"tool-out-1","call_id":"call-2"}]}`)

	deduped := dedupeResponsesWebsocketInputItemsByID(payload)

	items := gjson.GetBytes(deduped, "input").Array()
	if len(items) != 2 {
		t.Fatalf("deduped input len = %d, want 2: %s", len(items), deduped)
	}
	if items[0].Get("id").String() != "ctc-1" ||
		items[0].Get("call_id").String() != "call-2" ||
		items[1].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected deduped input: %s", deduped)
	}
}

func TestDedupeResponsesWebsocketInputItemsByIDKeepsReferencedToolCall(t *testing.T) {
	// Two function_call items share the same id but carry different call_ids
	// (e.g. the upstream reused the item id across a re-sent/repaired call).
	// Only the first call_id has a matching function_call_output. Deduping by
	// id must keep the referenced call so the output is not orphaned, which
	// previously triggered an upstream 400 "No tool call found for function
	// call output with call_id ...".
	payload := []byte(`{"input":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"exec_command"},{"type":"function_call","id":"fc-1","call_id":"call-2","name":"exec_command"},{"type":"function_call_output","id":"fco-1","call_id":"call-1"}]}`)

	deduped := dedupeResponsesWebsocketInputItemsByID(payload)

	items := gjson.GetBytes(deduped, "input").Array()
	if len(items) != 2 {
		t.Fatalf("deduped input len = %d, want 2: %s", len(items), deduped)
	}
	if items[0].Get("id").String() != "fc-1" ||
		items[0].Get("call_id").String() != "call-1" ||
		items[1].Get("id").String() != "fco-1" ||
		items[1].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected deduped input: %s", deduped)
	}
}

func TestResponsesWebsocketCompactionResetsTurnStateOnCustomToolTranscriptReplacement(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCompactionCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-sse", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	router.POST("/v1/responses/compact", h.Compact)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"custom_tool_call_output","call_id":"call-1","id":"tool-out-1"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	compactResp, errPost := server.Client().Post(
		server.URL+"/v1/responses/compact",
		"application/json",
		strings.NewReader(`{"model":"test-model","input":[{"type":"message","id":"summary-1"}]}`),
	)
	if errPost != nil {
		t.Fatalf("compact request failed: %v", errPost)
	}
	if errClose := compactResp.Body.Close(); errClose != nil {
		t.Fatalf("close compact response body: %v", errClose)
	}
	if compactResp.StatusCode != http.StatusOK {
		t.Fatalf("compact status = %d, want %d", compactResp.StatusCode, http.StatusOK)
	}

	postCompact := `{"type":"response.create","input":[{"type":"custom_tool_call","id":"ctc-compact","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-compact","call_id":"call-1"},{"type":"message","id":"msg-2"}]}`
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(postCompact)); errWrite != nil {
		t.Fatalf("write post-compact websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read post-compact websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("post-compact payload type = %s, want %s", got, wsEventTypeCompleted)
	}

	executor.mu.Lock()
	defer executor.mu.Unlock()

	if executor.compactPayload == nil {
		t.Fatalf("compact payload was not captured")
	}
	if len(executor.streamPayloads) != 3 {
		t.Fatalf("stream payload count = %d, want 3", len(executor.streamPayloads))
	}

	merged := executor.streamPayloads[2]
	items := gjson.GetBytes(merged, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), merged)
	}
	if items[0].Get("id").String() != "ctc-compact" ||
		items[1].Get("id").String() != "tool-out-compact" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected post-compact input order: %s", merged)
	}
	if items[0].Get("call_id").String() != "call-1" {
		t.Fatalf("post-compact custom tool call id = %s, want call-1", items[0].Get("call_id").String())
	}
}

func TestResponsesWebsocketCompactionResetsTurnStateOnTranscriptReplacement(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCompactionCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-sse", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	router.POST("/v1/responses/compact", h.Compact)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	compactResp, errPost := server.Client().Post(
		server.URL+"/v1/responses/compact",
		"application/json",
		strings.NewReader(`{"model":"test-model","input":[{"type":"message","id":"summary-1"}]}`),
	)
	if errPost != nil {
		t.Fatalf("compact request failed: %v", errPost)
	}
	if errClose := compactResp.Body.Close(); errClose != nil {
		t.Fatalf("close compact response body: %v", errClose)
	}
	if compactResp.StatusCode != http.StatusOK {
		t.Fatalf("compact status = %d, want %d", compactResp.StatusCode, http.StatusOK)
	}

	// Simulate a post-compaction client turn that replaces local history with a compacted transcript.
	// The websocket handler must treat this as a state reset, not append it to stale pre-compaction state.
	postCompact := `{"type":"response.create","input":[{"type":"function_call","id":"fc-compact","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-2"}]}`
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(postCompact)); errWrite != nil {
		t.Fatalf("write post-compact websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read post-compact websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("post-compact payload type = %s, want %s", got, wsEventTypeCompleted)
	}

	executor.mu.Lock()
	defer executor.mu.Unlock()

	if executor.compactPayload == nil {
		t.Fatalf("compact payload was not captured")
	}
	if len(executor.streamPayloads) != 3 {
		t.Fatalf("stream payload count = %d, want 3", len(executor.streamPayloads))
	}

	merged := executor.streamPayloads[2]
	items := gjson.GetBytes(merged, "input").Array()
	if len(items) != 2 {
		t.Fatalf("merged input len = %d, want 2: %s", len(items), merged)
	}
	if items[0].Get("id").String() != "fc-compact" ||
		items[1].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected post-compact input order: %s", merged)
	}
	if items[0].Get("call_id").String() != "call-1" {
		t.Fatalf("post-compact function call id = %s, want call-1", items[0].Get("call_id").String())
	}
}

func TestInputContainsFullTranscriptFalseForAssistantMessageOnly(t *testing.T) {
	input := gjson.Parse(`[
		{"type":"message","role":"user","content":"hello"},
		{"type":"message","role":"assistant","content":"hi there"}
	]`)
	if inputContainsFullTranscript(input) {
		t.Fatal("assistant message alone must not be treated as full transcript")
	}
}

func TestInputContainsFullTranscriptDetectsCompactionItem(t *testing.T) {
	for _, typ := range []string{"compaction", "compaction_summary"} {
		input := gjson.Parse(`[{"type":"message","role":"user","content":"hello"},{"type":"` + typ + `","encrypted_content":"summary"}]`)
		if !inputContainsFullTranscript(input) {
			t.Fatalf("expected full transcript for type=%s", typ)
		}
	}
}

func TestInputContainsFullTranscriptFalseForIncremental(t *testing.T) {
	// Normal incremental turns: user messages or function_call_output only.
	for _, raw := range []string{
		`[{"type":"function_call_output","call_id":"call-1","output":"result"}]`,
		`[{"type":"message","role":"user","content":"next question"}]`,
		`[]`,
	} {
		if inputContainsFullTranscript(gjson.Parse(raw)) {
			t.Fatalf("incremental input must not be detected as full transcript: %s", raw)
		}
	}
}

func TestNormalizeSubsequentRequestCompactSkipsMerge(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[
		{"type":"message","role":"user","id":"msg-1","content":"original long prompt"},
		{"type":"message","role":"assistant","id":"msg-2","content":"original long response"},
		{"type":"function_call","id":"fc-1","call_id":"call-old","name":"bash","arguments":"{}"},
		{"type":"function_call_output","id":"fco-1","call_id":"call-old","output":"old result"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","role":"assistant","id":"msg-3","content":"another assistant reply"},
		{"type":"function_call","id":"fc-2","call_id":"call-stale","name":"read","arguments":"{}"}
	]`)

	// Remote compact response: user messages + compaction item, NO assistant message.
	// This is the primary compact scenario from Codex CLI.
	raw := []byte(`{"type":"response.create","input":[
		{"type":"message","role":"user","id":"msg-1c","content":"compacted user msg"},
		{"type":"compaction","encrypted_content":"conversation summary"}
	]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 2 {
		t.Fatalf("input len = %d, want 2 (compacted only); stale state was not skipped", len(input))
	}
	if input[0].Get("id").String() != "msg-1c" {
		t.Fatalf("input[0].id = %q, want %q", input[0].Get("id").String(), "msg-1c")
	}
	if input[1].Get("type").String() != "compaction" {
		t.Fatalf("input[1].type = %q, want %q", input[1].Get("type").String(), "compaction")
	}
}

func TestNormalizeSubsequentRequestReasoningContinuationWithPreviousResponseID(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.6-terra","stream":true,"input":[{"type":"message","role":"user","id":"old-user","content":"long history"}]}`)
	lastResponseOutput := []byte(`[{"type":"function_call","id":"old-call","call_id":"old-call","name":"lookup","arguments":"{}"}]`)

	for _, requestType := range []string{"response.create", "response.append"} {
		t.Run(requestType, func(t *testing.T) {
			raw := []byte(`{"type":"` + requestType + `","previous_response_id":"resp_1","input":[
				{"type":"reasoning","id":"reasoning-1","summary":[]},
				{"type":"function_call_output","id":"output-1","call_id":"old-call","output":"result"}
			]}`)

			normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
			if errMsg != nil {
				t.Fatalf("unexpected error: %v", errMsg.Error)
			}
			if got := gjson.GetBytes(normalized, "previous_response_id").String(); got != "resp_1" {
				t.Fatalf("previous_response_id = %q, want resp_1; payload=%s", got, normalized)
			}
			input := gjson.GetBytes(normalized, "input").Array()
			if len(input) != 2 || input[0].Get("id").String() != "reasoning-1" || input[1].Get("id").String() != "output-1" {
				t.Fatalf("incremental continuation was replaced or merged: %s", normalized)
			}
		})
	}
}

func TestResponsesWebsocketOutputCollectorRestoresCompletedOutput(t *testing.T) {
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	for _, payload := range [][]byte{
		[]byte(`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"reply-1","role":"assistant"}}`),
		[]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"summary-1","summary":[]}}`),
		[]byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"call-1","call_id":"call-1"}}`),
	} {
		collectResponsesWebsocketOutputItem(payload, outputItemsByIndex, &outputItemsFallback)
	}

	output := responseCompletedOutputFromPayload(
		[]byte(`{"type":"response.completed","response":{"id":"resp-1","output":[]}}`),
		outputItemsByIndex,
		outputItemsFallback,
	)
	items := gjson.ParseBytes(output).Array()
	if len(items) != 3 {
		t.Fatalf("collected output len = %d, want 3: %s", len(items), output)
	}
	wantIDs := []string{"summary-1", "reply-1", "call-1"}
	for i, wantID := range wantIDs {
		if got := items[i].Get("id").String(); got != wantID {
			t.Fatalf("output[%d].id = %q, want %q: %s", i, got, wantID, output)
		}
	}
}

func TestNormalizeSubsequentRequestCompactMergesWhenCompactionReplayUnsupported(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[
		{"type":"message","role":"user","id":"msg-1","content":"original long prompt"},
		{"type":"message","role":"assistant","id":"msg-2","content":"original long response"},
		{"type":"function_call","id":"fc-1","call_id":"call-old","name":"bash","arguments":"{}"},
		{"type":"function_call_output","id":"fco-1","call_id":"call-old","output":"old result"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","role":"assistant","id":"msg-3","content":"another assistant reply"},
		{"type":"function_call","id":"fc-2","call_id":"call-stale","name":"read","arguments":"{}"}
	]`)
	raw := []byte(`{"type":"response.create","input":[
		{"type":"message","role":"user","id":"msg-1c","content":"compacted user msg"},
		{"type":"compaction","encrypted_content":"conversation summary"}
	]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 7 {
		t.Fatalf("input len = %d, want 7 (merged fallback without compaction items)", len(input))
	}
	wantIDs := []string{"msg-1", "msg-2", "fc-1", "fco-1", "msg-3", "fc-2", "msg-1c"}
	for i, want := range wantIDs {
		got := input[i].Get("id").String()
		if got != want {
			t.Fatalf("input[%d].id = %q, want %q", i, got, want)
		}
	}
	for _, item := range input {
		if item.Get("type").String() == "compaction" || item.Get("type").String() == "compaction_summary" {
			t.Fatalf("compaction items must be stripped for unsupported downstream fallback: %s", item.Raw)
		}
	}
}

func TestNormalizeSubsequentRequestDropsStaleCompactionTrigger(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[
		{"type":"message","role":"user","id":"msg-1","content":"original prompt"},
		{"type":"compaction_trigger","id":"ct-stale"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","role":"assistant","id":"msg-2","content":"compacted"}
	]`)
	raw := []byte(`{"type":"response.create","input":[
		{"type":"message","role":"user","id":"msg-3","content":"next prompt"}
	]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 3 {
		t.Fatalf("input len = %d, want 3 without stale compaction_trigger: %s", len(input), normalized)
	}
	for _, item := range input {
		if item.Get("type").String() == "compaction_trigger" {
			t.Fatalf("stale compaction_trigger must not be replayed before later input: %s", normalized)
		}
	}
	if got := input[2].Get("id").String(); got != "msg-3" {
		t.Fatalf("input[2].id = %q, want msg-3; payload=%s", got, normalized)
	}
}

func TestNormalizeSubsequentRequestKeepsCurrentCompactionTriggerFinal(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[
		{"type":"message","role":"user","id":"msg-1","content":"original prompt"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","role":"assistant","id":"msg-2","content":"answer"}
	]`)
	raw := []byte(`{"type":"response.create","input":[
		{"type":"compaction_trigger","id":"ct-current"},
		{"type":"message","role":"user","id":"msg-3","content":"include this before compacting"}
	]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("input len = %d, want 4: %s", len(input), normalized)
	}
	if got := input[2].Get("id").String(); got != "msg-3" {
		t.Fatalf("input[2].id = %q, want msg-3 before compaction_trigger; payload=%s", got, normalized)
	}
	if got := input[3].Get("type").String(); got != "compaction_trigger" {
		t.Fatalf("last input type = %q, want compaction_trigger; payload=%s", got, normalized)
	}
}

func TestNormalizeSubsequentRequestIncrementalInputStillMerges(t *testing.T) {
	// Normal incremental flow: user sends function_call_output (no assistant message).
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[
		{"type":"message","role":"user","id":"msg-1","content":"hello"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","role":"assistant","id":"msg-2","content":"let me check"},
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"bash","arguments":"{}"}
	]`)
	raw := []byte(`{"type":"response.create","input":[
		{"type":"function_call_output","call_id":"call-1","id":"fco-1","output":"done"}
	]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()

	// Should be merged: msg-1 + msg-2 + fc-1 + fco-1 = 4 items
	if len(input) != 4 {
		t.Fatalf("input len = %d, want 4 (merged)", len(input))
	}
	wantIDs := []string{"msg-1", "msg-2", "fc-1", "fco-1"}
	for i, want := range wantIDs {
		got := input[i].Get("id").String()
		if got != want {
			t.Fatalf("input[%d].id = %q, want %q", i, got, want)
		}
	}
}

func TestNormalizeSubsequentRequestAssistantInputTriggersTranscriptReplacement(t *testing.T) {
	// After dev's shouldReplaceWebsocketTranscript, assistant messages in input
	// trigger transcript replacement (no merge with prior state).
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[
		{"type":"message","role":"user","id":"msg-1","content":"hello"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","role":"assistant","id":"msg-2","content":"prior assistant"},
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"bash","arguments":"{}"}
	]`)
	raw := []byte(`{"type":"response.append","input":[
		{"type":"message","role":"assistant","id":"msg-3","content":"patched assistant turn"}
	]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1 (transcript replacement, not merge)", len(input))
	}
	if input[0].Get("id").String() != "msg-3" {
		t.Fatalf("input[0].id = %q, want %q", input[0].Get("id").String(), "msg-3")
	}
}

// TestResponsesWebsocketHeartbeatSendsPing 验证 startResponsesWebsocketHeartbeat
// 会定期向下游客户端发送 websocket ping frame，防止"正在思考"卡住。
func TestResponsesWebsocketHeartbeatSendsPing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		done := make(chan struct{})
		defer func() {
			close(done)
			_ = conn.Close()
		}()
		startResponsesWebsocketHeartbeat(conn, done, "test-session", 20*time.Millisecond)
		<-releaseServer
	}))
	defer server.Close()
	defer close(releaseServer)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	pingCh := make(chan string, 1)
	conn.SetPingHandler(func(appData string) error {
		select {
		case pingCh <- appData:
		default:
		}
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
	})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}()
	select {
	case got := <-pingCh:
		if got != "ping" {
			t.Fatalf("ping payload = %q, want ping", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for websocket ping")
	}
	_ = conn.Close()
	<-readDone
}

func TestForwardResponsesWebsocketSendsApplicationHeartbeatWhileWaiting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseForward := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseForward()

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.Close() }()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r
		data := make(chan []byte, 1)
		errs := make(chan *interfaces.ErrorMessage)
		go func() {
			<-release
			data <- []byte(`{"type":"response.completed","response":{"id":"resp-heartbeat","output":[{"type":"message","id":"msg-1"}]}}`)
			close(data)
			close(errs)
		}()

		base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
			Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1},
		}, nil)
		h := NewOpenAIResponsesAPIHandler(base)
		_, _, _, errMsg, _, err := h.forwardResponsesWebsocket(ctx, conn, func(...interface{}) {}, data, errs, nil, "session-heartbeat", false, nil, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		if errMsg != nil {
			serverErrCh <- fmt.Errorf("unexpected error message: %v", errMsg.Error)
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	_, payload, err := conn.ReadMessage()
	if err != nil {
		releaseForward()
		<-serverErrCh
		t.Fatalf("read application heartbeat: %v", err)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != "response.in_progress" {
		releaseForward()
		<-serverErrCh
		t.Fatalf("heartbeat type = %q, want response.in_progress; payload=%s", got, payload)
	}

	releaseForward()
	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestResponsesWebsocketSendsHeartbeatAndReturnsAfterClientCloseDuringBootstrap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseExecutor := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseExecutor()

	executor := &websocketBlockingExecutor{
		started:  started,
		canceled: canceled,
		release:  release,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-blocking", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1},
	}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	handlerDone := make(chan struct{})
	router.GET("/v1/responses/ws", func(c *gin.Context) {
		h.ResponsesWebsocket(c)
		close(handlerDone)
	})
	server := httptest.NewServer(router)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","input":[{"type":"message","role":"user","content":"hello"}]}`)); errWrite != nil {
		_ = conn.Close()
		t.Fatalf("write request: %v", errWrite)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		_ = conn.Close()
		t.Fatal("executor did not start")
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, errRead := conn.ReadMessage()
	if errRead != nil {
		_ = conn.Close()
		t.Fatalf("read application heartbeat during stream bootstrap: %v", errRead)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != "response.in_progress" {
		_ = conn.Close()
		t.Fatalf("bootstrap heartbeat type = %q, want response.in_progress; payload=%s", got, payload)
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"), time.Now().Add(time.Second))
	_ = conn.Close()

	select {
	case <-handlerDone:
	case <-time.After(500 * time.Millisecond):
		releaseExecutor()
		t.Fatal("websocket handler did not return after downstream client closed")
	}
	select {
	case <-canceled:
	case <-time.After(500 * time.Millisecond):
		releaseExecutor()
		t.Fatal("synchronous stream bootstrap was not canceled after downstream client closed")
	}
}

func TestResponsesWebsocketContinuesAfterSynchronousBootstrap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseExecutor := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseExecutor()

	executor := &websocketBlockingExecutor{
		started: started,
		release: release,
		payload: []byte(`{"type":"response.completed","response":{"id":"resp-bootstrap","output":[{"type":"message","id":"msg-bootstrap"}]}}`),
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-bootstrap-complete", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-bootstrap-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1},
	}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	handlerDone := make(chan struct{})
	router.GET("/v1/responses/ws", func(c *gin.Context) {
		h.ResponsesWebsocket(c)
		close(handlerDone)
	})
	server := httptest.NewServer(router)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-bootstrap-model","input":[{"type":"message","role":"user","content":"hello"}]}`)); errWrite != nil {
		t.Fatalf("write request: %v", errWrite)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, heartbeat, errReadHeartbeat := conn.ReadMessage()
	if errReadHeartbeat != nil {
		t.Fatalf("read bootstrap heartbeat: %v", errReadHeartbeat)
	}
	if got := gjson.GetBytes(heartbeat, "type").String(); got != "response.in_progress" {
		t.Fatalf("bootstrap heartbeat type = %q, want response.in_progress; payload=%s", got, heartbeat)
	}

	releaseExecutor()
	_, completed, errReadCompleted := conn.ReadMessage()
	if errReadCompleted != nil {
		t.Fatalf("read response.completed after bootstrap: %v", errReadCompleted)
	}
	if got := gjson.GetBytes(completed, "type").String(); got != "response.completed" {
		t.Fatalf("event after bootstrap = %q, want response.completed; payload=%s", got, completed)
	}

	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"), time.Now().Add(time.Second))
	_ = conn.Close()
	select {
	case <-handlerDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("websocket handler did not return after completed bootstrap session closed")
	}
}

func TestResponsesWebsocketStatePreflightAllowsReplacementAndIncrementalRecovery(t *testing.T) {
	lastRequest := bytes.Repeat([]byte("x"), 100)
	lastOutput := bytes.Repeat([]byte("y"), 100)
	replacement := []byte(`{"type":"response.create","input":[{"type":"message","role":"assistant","content":"compact"}]}`)
	if !responsesWebsocketStatePreflight(replacement, lastRequest, lastOutput, false, false, 128) {
		t.Fatal("replacement request was falsely rejected by state preflight")
	}
	incremental := []byte(`{"type":"response.create","previous_response_id":"resp_1","input":[]}`)
	if !responsesWebsocketStatePreflight(incremental, lastRequest, lastOutput, true, false, 128) {
		t.Fatal("valid previous_response_id request was falsely rejected by state preflight")
	}
	appendRequest := []byte(`{"type":"response.append","input":[{"type":"message","role":"user"}]}`)
	if responsesWebsocketStatePreflight(appendRequest, lastRequest, lastOutput, false, false, 128) {
		t.Fatal("projected merged state exceeding limit was accepted")
	}
	fullTranscript := []byte(`{"type":"response.create","input":[{"type":"message","role":"user"},{"type":"message","role":"assistant"}]}`)
	if !responsesWebsocketStatePreflight(fullTranscript, lastRequest, lastOutput, false, true, 128) {
		t.Fatal("supported full transcript bypass was falsely rejected")
	}
	if !responsesWebsocketStateWithinLimit(bytes.Repeat([]byte("r"), 64), bytes.Repeat([]byte("o"), 64), 128) {
		t.Fatal("exact state at limit was rejected")
	}
	if responsesWebsocketStateWithinLimit(bytes.Repeat([]byte("r"), 65), bytes.Repeat([]byte("o"), 64), 128) {
		t.Fatal("exact state above limit was accepted")
	}
}

func TestResponsesWebsocketBudgetErrorPayloadUsesStructuredCode(t *testing.T) {
	payload := buildResponsesWebsocketBudgetErrorPayload("websocket_session_state_limit_exceeded", "too large")
	if got := gjson.GetBytes(payload, "type").String(); got != "response.failed" {
		t.Fatalf("type = %q, payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.error.code").String(); got != "websocket_session_state_limit_exceeded" {
		t.Fatalf("code = %q, payload=%s", got, payload)
	}
}

func TestResponsesWebsocketOutputAccumulatorRejectsCumulativeLimit(t *testing.T) {
	acc := newResponsesWebsocketOutputAccumulatorWithLimit(20)
	if !acc.AppendOutputItemDone([]byte(`{"item":{"x":"1234"}}`)) {
		t.Fatal("first output item rejected")
	}
	if acc.AppendOutputItemDone([]byte(`{"item":{"x":"5678901234"}}`)) {
		t.Fatal("cumulative output over limit was accepted")
	}
	if got := string(acc.Output()); got != `[{"x":"1234"}]` {
		t.Fatalf("output after rejection = %s", got)
	}
}

func TestWebsocketToolOutputCacheEnforcesByteBudgetAndOverwriteAccounting(t *testing.T) {
	cache := newWebsocketToolOutputCacheWithBytes(time.Minute, 10, 12)
	cache.record("session", "a", json.RawMessage(`{"x":"1"}`))
	cache.record("session", "b", json.RawMessage(`{"x":"2"}`))
	if _, ok := cache.get("session", "a"); ok {
		t.Fatal("oldest entry was not evicted to fit byte budget")
	}
	cache.record("session", "b", json.RawMessage(`{"x":"22"}`))
	if got, ok := cache.get("session", "b"); !ok || string(got) != `{"x":"22"}` {
		t.Fatalf("overwritten entry = %s, ok=%v", got, ok)
	}
	cache.record("session", "huge", json.RawMessage(bytes.Repeat([]byte("x"), 13)))
	if _, ok := cache.get("session", "huge"); ok {
		t.Fatal("single entry larger than byte budget was cached")
	}
}

func TestDefaultWebsocketToolCachesEnableTTL(t *testing.T) {
	if defaultWebsocketToolOutputCache.ttl != websocketToolOutputCacheTTL || defaultWebsocketToolCallCache.ttl != websocketToolOutputCacheTTL {
		t.Fatalf("default cache TTLs = %v/%v, want %v", defaultWebsocketToolOutputCache.ttl, defaultWebsocketToolCallCache.ttl, websocketToolOutputCacheTTL)
	}
}

func TestWebsocketToolOutputCacheClearPreservesSessionByteLimit(t *testing.T) {
	cache := newWebsocketToolOutputCacheWithBytes(time.Minute, 10, 100)
	cache.setSessionMaxBytes("session", 4)
	cache.record("session", "small", json.RawMessage(`1234`))
	cache.clearSessionItems("session")
	if _, ok := cache.get("session", "small"); ok {
		t.Fatal("rejected turn cache item remained after clear")
	}
	cache.record("session", "too-large", json.RawMessage(`12345`))
	if _, ok := cache.get("session", "too-large"); ok {
		t.Fatal("session-specific byte limit was lost after cache clear")
	}
	cache.mu.Lock()
	session := cache.sessions["session"]
	cache.mu.Unlock()
	if session == nil || session.maxBytes != 4 {
		t.Fatalf("session maxBytes after clear = %+v", session)
	}
}

func TestResponsesWebsocketPerCacheBudgetSplitsCombinedLimit(t *testing.T) {
	if got := responsesWebsocketPerCacheBudget(8 << 20); got != 4<<20 {
		t.Fatalf("per-cache budget = %d, want %d", got, int64(4<<20))
	}
}

func TestResponsesWebsocketStateReservationChargesSum(t *testing.T) {
	budget := &responsesWebsocketMemoryBudget{}
	reservation := budget.newReservation()
	if !resizeResponsesWebsocketStateReservation(reservation, 100, 60) {
		t.Fatal("60-byte state did not fit into 100-byte budget")
	}
	if resizeResponsesWebsocketStateReservation(reservation, 100, 110) {
		t.Fatal("110-byte state should not fit into 100-byte budget")
	}
	reservation.release()
}

func TestResponsesWebsocketDefaultBudgetAllowsOneMaximumTransitionButRejectsTwo(t *testing.T) {
	budget := &responsesWebsocketMemoryBudget{}
	first := budget.newReservation()
	second := budget.newReservation()
	const max = int64(32 << 20)
	if !resizeResponsesWebsocketTransitionReservation(first, sdkconfig.DefaultResponsesWebsocketMemoryBudgetBytes, max, max, max, max) {
		t.Fatal("one maximum old-to-new state transition was rejected by default websocket budget")
	}
	if resizeResponsesWebsocketTransitionReservation(second, sdkconfig.DefaultResponsesWebsocketMemoryBudgetBytes, max, max, max, max) {
		t.Fatal("two maximum transitions exceeded default websocket aggregate budget but were accepted")
	}
	first.release()
	second.release()
}

func TestResponsesWebsocketAggregateBudgetAccountsForOldAndNewTurnState(t *testing.T) {
	budget := &responsesWebsocketMemoryBudget{}
	first := budget.newReservation()
	second := budget.newReservation()
	const mib = int64(1 << 20)
	const limit = 192 * mib

	reserveTransition := func(reservation *responsesWebsocketMemoryReservation) bool {
		return resizeResponsesWebsocketTransitionReservation(reservation, limit, 20*mib, 20*mib, 40*mib, 24*mib)
	}
	if !reserveTransition(first) {
		t.Fatal("first 104 MiB old-to-new state transition was rejected")
	}
	if reserveTransition(second) {
		t.Fatal("two overlapping 104 MiB transitions exceeded the 192 MiB aggregate budget but were accepted")
	}
	first.release()
	second.release()
}

func TestAccumulateResponsesWebsocketPayloadRejectsBeforeCollectorClone(t *testing.T) {
	acc := newResponsesWebsocketOutputAccumulatorWithLimit(8)
	byIndex := make(map[int64][]byte)
	var fallback [][]byte
	payload := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"text":"oversized"}}`)
	_, ok := accumulateResponsesWebsocketPayload(payload, acc, byIndex, &fallback)
	if ok {
		t.Fatal("oversized output item was accepted")
	}
	if len(byIndex) != 0 || len(fallback) != 0 {
		t.Fatalf("collector cloned rejected item: byIndex=%d fallback=%d", len(byIndex), len(fallback))
	}
}

func TestResponsesWebsocketMemoryBudgetContendsAndReleases(t *testing.T) {
	budget := &responsesWebsocketMemoryBudget{}
	first := budget.newReservation()
	second := budget.newReservation()
	if !first.resize(60, 100) {
		t.Fatal("first session reservation rejected")
	}
	if second.resize(50, 100) {
		t.Fatal("aggregate websocket state budget accepted overcommit")
	}
	first.release()
	if !second.resize(50, 100) {
		t.Fatal("released websocket state budget was not reusable")
	}
	second.release()
	if got := budget.inUseBytes(); got != 0 {
		t.Fatalf("budget in use after release = %d", got)
	}
}

func TestResponsesWebsocketDefaultDoesNotLimitConcurrentConnections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	const connectionCount = 8
	connections := make([]*websocket.Conn, 0, connectionCount)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()

	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	for i := 0; i < connectionCount; i++ {
		conn, response, err := websocket.DefaultDialer.Dial(websocketURL, nil)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			t.Fatalf("connection %d failed: status=%d err=%v", i+1, status, err)
		}
		connections = append(connections, conn)
	}
}

func TestResponsesWebsocketPositiveLimitStillRejectsExcessConnections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{ResponsesWebsocketMaxConnections: 1}, nil)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	first, _, err := websocket.DefaultDialer.Dial(websocketURL, nil)
	if err != nil {
		t.Fatalf("first connection failed: %v", err)
	}
	defer func() { _ = first.Close() }()

	second, response, err := websocket.DefaultDialer.Dial(websocketURL, nil)
	if second != nil {
		_ = second.Close()
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("second connection unexpectedly bypassed the explicit limit")
	}
	if response == nil || response.StatusCode != http.StatusServiceUnavailable {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("second connection status = %d, want %d", status, http.StatusServiceUnavailable)
	}
}

func TestResponsesWebsocketConnectionLimiterTracksUnlimitedConnections(t *testing.T) {
	limiter := &responsesWebsocketConnectionLimiter{}
	const connectionCount = 8
	for i := 0; i < connectionCount; i++ {
		if !limiter.tryAcquire(0) {
			t.Fatalf("unlimited connection %d was rejected", i+1)
		}
	}
	if got := limiter.current.Load(); got != connectionCount {
		t.Fatalf("tracked connections = %d, want %d", got, connectionCount)
	}
	for i := 0; i < connectionCount; i++ {
		limiter.release()
	}
	if got := limiter.current.Load(); got != 0 {
		t.Fatalf("tracked connections after release = %d, want 0", got)
	}
}

func TestResponsesWebsocketConnectionLimiterReleases(t *testing.T) {
	limiter := &responsesWebsocketConnectionLimiter{}
	if !limiter.tryAcquire(1) {
		t.Fatal("first connection rejected")
	}
	if limiter.tryAcquire(1) {
		t.Fatal("second connection exceeded configured limit")
	}
	limiter.release()
	if !limiter.tryAcquire(1) {
		t.Fatal("connection slot was not released")
	}
	limiter.release()
}

func TestResponsesWebsocketTurnLimitAtSessionBoundaryRejectsBeforeUpstream(t *testing.T) {
	limit := responsesWebsocketEffectiveTurnOutputLimit(32, 64, 64)
	if limit != 0 || responsesWebsocketCanStartTurn(limit) {
		t.Fatalf("effective limit = %d, turn should be rejected before upstream", limit)
	}
	acc := newResponsesWebsocketOutputAccumulatorWithLimit(limit)
	if acc.SetCompleted([]byte(`{"type":"response.completed","response":{"output":[]}}`)) {
		t.Fatal("strict zero accumulator accepted minimum completed output")
	}
}

func TestResponsesWebsocketTurnLimitFailureDoesNotWriteCompleted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r
		setResponsesWebsocketTurnOutputLimit(ctx, 8)
		data := make(chan []byte, 2)
		errs := make(chan *interfaces.ErrorMessage)
		data <- []byte(`{"type":"response.output_item.done","output_index":0,"item":{"text":"oversized"}}`)
		data <- []byte(`{"type":"response.completed","response":{"output":[]}}`)
		close(data)
		close(errs)
		base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
		h := NewOpenAIResponsesAPIHandler(base)
		_, _, _, errMsg, _, err := h.forwardResponsesWebsocket(ctx, conn, func(...interface{}) {}, data, errs, nil, "session", false, nil, nil)
		if err != nil {
			serverErr <- err
			return
		}
		if !isResponsesWebsocketBudgetError(errMsg) {
			serverErr <- fmt.Errorf("error = %v, want websocket budget error", errMsg)
			return
		}
		serverErr <- nil
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != "response.failed" {
		t.Fatalf("first payload type = %q, payload=%s", got, payload)
	}
	if strings.Contains(string(payload), "response.completed") {
		t.Fatalf("completed was written before failure: %s", payload)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestResponsesWebsocketTurnLimitsSnapshotIsStableAcrossConfigChange(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ResponsesWebsocketMaxSessionBytes:    100,
		ResponsesWebsocketMaxTurnOutputBytes: 40,
		ResponsesWebsocketMemoryBudgetBytes:  200,
		ResponsesWebsocketToolCacheBytes:     20,
	}
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, nil))
	first := h.responsesWebsocketTurnLimitsSnapshot()
	h.Cfg.ResponsesWebsocketMaxSessionBytes = 80
	h.Cfg.ResponsesWebsocketMaxTurnOutputBytes = 30
	second := h.responsesWebsocketTurnLimitsSnapshot()
	if first.sessionBytes != 100 || first.turnOutputBytes != 40 || first.memoryBudgetBytes != 200 || first.toolCacheBytes != 20 {
		t.Fatalf("first snapshot changed: %+v", first)
	}
	if second.sessionBytes != 80 || second.turnOutputBytes != 30 {
		t.Fatalf("second snapshot did not refresh: %+v", second)
	}
}

func TestShouldReplayResponsesWebsocketTranscriptNoToolCallFound(t *testing.T) {
	cases := []struct {
		name    string
		message string
		status  int
		want    bool
	}{
		{
			name:    "custom tool call output orphan triggers replay",
			message: "No tool call found for custom tool call output with call_id call_1I9St3k5mUWsBkUEs9mFygMO.",
			status:  http.StatusBadRequest,
			want:    true,
		},
		{
			name:    "function call output orphan triggers replay",
			message: "No tool call found for function_call_output with call_id call_abc.",
			status:  http.StatusBadRequest,
			want:    true,
		},
		{
			name:    "empty tool call id triggers replay",
			message: "tool_call_id  is not found",
			status:  http.StatusBadRequest,
			want:    true,
		},
		{
			name:    "unrelated error does not trigger replay",
			message: "Invalid model: gpt-foo",
			status:  http.StatusBadRequest,
			want:    false,
		},
		{
			name:    "no tool call found but not 400 does not trigger replay",
			message: "No tool call found for custom tool call output with call_id call_x.",
			status:  http.StatusInternalServerError,
			want:    false,
		},
		{
			name:    "previous_response_not_found still triggers replay",
			message: "previous_response_id not found: resp_xxx",
			status:  http.StatusBadRequest,
			want:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errMsg := &interfaces.ErrorMessage{
				StatusCode: tc.status,
				Error:      errors.New(tc.message),
			}
			if got := shouldReplayResponsesWebsocketTranscript(errMsg); got != tc.want {
				t.Fatalf("shouldReplayResponsesWebsocketTranscript = %v, want %v (message=%q)", got, tc.want, tc.message)
			}
		})
	}
}

func TestShouldReplayZhipuMessagesValidation(t *testing.T) {
	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"code":"1214","message":"messages 参数非法。请检查文档。"}}`),
	}
	incrementalToolOutput := []byte(`{"previous_response_id":"resp_1","input":[{"type":"custom_tool_call_output","call_id":"call_1","output":"done"}]}`)

	cases := []struct {
		name          string
		provider      string
		request       []byte
		replayAttempt int
		errMsg        *interfaces.ErrorMessage
		want          bool
	}{
		{name: "bigmodel incremental tool output", provider: "bigmodel-coding", request: incrementalToolOutput, replayAttempt: 0, errMsg: errMsg, want: true},
		{name: "non bigmodel provider", provider: "test-provider", request: incrementalToolOutput, replayAttempt: 0, errMsg: errMsg, want: false},
		{name: "second replay attempt", provider: "bigmodel-coding", request: incrementalToolOutput, replayAttempt: 1, errMsg: errMsg, want: false},
		{name: "missing previous response id", provider: "bigmodel-coding", request: []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call_1","output":"done"}]}`), replayAttempt: 0, errMsg: errMsg, want: false},
		{name: "ordinary message", provider: "bigmodel-coding", request: []byte(`{"previous_response_id":"resp_1","input":[{"type":"message","role":"user","content":"hi"}]}`), replayAttempt: 0, errMsg: errMsg, want: false},
		{name: "different error code", provider: "bigmodel-coding", request: incrementalToolOutput, replayAttempt: 0, errMsg: &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New(`{"error":{"code":"1215","message":"messages 参数非法。请检查文档。"}}`)}, want: false},
		{name: "different error message", provider: "bigmodel-coding", request: incrementalToolOutput, replayAttempt: 0, errMsg: &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New(`{"error":{"code":"1214","message":"model 参数非法。请检查文档。"}}`)}, want: false},
		{name: "non bad request status", provider: "bigmodel-coding", request: incrementalToolOutput, replayAttempt: 0, errMsg: &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: errors.New(`{"error":{"code":"1214","message":"messages 参数非法。请检查文档。"}}`)}, want: false},
		{name: "malformed error body", provider: "bigmodel-coding", request: incrementalToolOutput, replayAttempt: 0, errMsg: &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New("1214 messages 参数非法")}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReplayZhipuMessagesValidation(tc.errMsg, tc.provider, tc.request, tc.replayAttempt); got != tc.want {
				t.Fatalf("shouldReplayZhipuMessagesValidation = %v, want %v", got, tc.want)
			}
		})
	}
}
