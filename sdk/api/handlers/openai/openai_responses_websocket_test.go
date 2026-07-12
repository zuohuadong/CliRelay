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
	mu       sync.Mutex
	authIDs  []string
	calls    map[string]int
	payloads map[string][][]byte
}

type websocketPreviousResponseReplayExecutor struct {
	mu       sync.Mutex
	calls    int
	payloads [][]byte
}

type websocketZeroOutputEOFReplayExecutor struct {
	mu                         sync.Mutex
	calls                      int
	failCalls                  int
	firstCallLifecyclePayloads bool
	failPayload                []byte
	payloads                   [][]byte
}

type websocketBootstrapFallbackExecutor struct {
	mu       sync.Mutex
	authIDs  []string
	payloads map[string][][]byte
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

	if authID == "auth-a" && call == 2 {
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

func (e *websocketPreviousResponseReplayExecutor) Identifier() string { return "test-provider" }

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
	if call == 2 {
		chunks <- coreexecutor.StreamChunk{Err: websocketPinnedFailoverStatusError{
			status: http.StatusBadRequest,
			msg:    `{"error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"Previous response with id 'resp_missing' not found.","param":"previous_response_id"}}`,
		}}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}

	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_replay_%d","output":[{"type":"message","id":"out-%d"}]}}`, call, call))}
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
		completedOutput, errMsg, _, err := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
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
		_, errMsg, _, err := h.forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
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
		completedOutput, errMsg, _, err := h.forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
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
		completedOutput, errMsg, _, err := h.forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
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
		completedOutput, errMsg, _, err := h.forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
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

		_, _, _, err = (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
			false,
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

func TestResponsesWebsocketStateReservationChargesTwoX(t *testing.T) {
	budget := &responsesWebsocketMemoryBudget{}
	reservation := budget.newReservation()
	if resizeResponsesWebsocketStateReservation(reservation, 100, 60) {
		t.Fatal("60-byte state fit into 100-byte budget despite 2x charge")
	}
	if !resizeResponsesWebsocketStateReservation(reservation, 120, 60) {
		t.Fatal("60-byte state did not fit exact 120-byte estimated budget")
	}
	reservation.release()
}

func TestResponsesWebsocketDefaultBudgetAllowsOneMaximumTurnButRejectsTwo(t *testing.T) {
	budget := &responsesWebsocketMemoryBudget{}
	first := budget.newReservation()
	second := budget.newReservation()
	const max = int64(32 << 20)
	if !resizeResponsesWebsocketStateReservation(first, sdkconfig.DefaultResponsesWebsocketMemoryBudgetBytes, 2, max, max) {
		t.Fatal("one maximum request/output turn was rejected by default websocket budget")
	}
	if resizeResponsesWebsocketStateReservation(second, sdkconfig.DefaultResponsesWebsocketMemoryBudgetBytes, 2, max, max) {
		t.Fatal("two maximum turns exceeded default websocket aggregate budget but were accepted")
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
		_, errMsg, _, err := h.forwardResponsesWebsocket(ctx, conn, func(...interface{}) {}, data, errs, nil, "session", false)
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
