package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type codexSearchCaptureExecutor struct {
	request      *http.Request
	body         []byte
	authIDs      []string
	responseBody io.ReadCloser
	statuses     []int
	httpCalls    int
}

func (e *codexSearchCaptureExecutor) Identifier() string { return "codex" }
func (e *codexSearchCaptureExecutor) Execute(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e *codexSearchCaptureExecutor) ExecuteStream(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}
func (e *codexSearchCaptureExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a.Clone(), nil
}
func (e *codexSearchCaptureExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e *codexSearchCaptureExecutor) PrepareRequest(req *http.Request, a *auth.Auth) error {
	if token, _ := a.Metadata["access_token"].(string); strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return nil
}
func (e *codexSearchCaptureExecutor) HttpRequest(_ context.Context, selected *auth.Auth, req *http.Request) (*http.Response, error) {
	e.request = req.Clone(req.Context())
	e.authIDs = append(e.authIDs, selected.ID)
	e.httpCalls++
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	e.body = body
	responseBody := e.responseBody
	if responseBody == nil {
		responseBody = io.NopCloser(strings.NewReader(`{"results":[{"url":"https://example.com"}]}`))
	}
	statusCode := http.StatusOK
	if e.httpCalls <= len(e.statuses) && e.statuses[e.httpCalls-1] > 0 {
		statusCode = e.statuses[e.httpCalls-1]
	}
	return &http.Response{StatusCode: statusCode, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: responseBody}, nil
}

type codexSearchHomeDispatcher struct {
	calls atomic.Int32
}

func (*codexSearchHomeDispatcher) HeartbeatOK() bool { return true }
func (d *codexSearchHomeDispatcher) RPopAuth(_ context.Context, model, _ string, _ http.Header, _ int) ([]byte, error) {
	d.calls.Add(1)
	return json.Marshal(map[string]any{
		"model": model,
		"auth_index": "home-codex-search",
		"auth": map[string]any{
			"id": "home-codex-search", "provider": "codex", "status": "active",
			"metadata": map[string]any{"access_token": "home-search-token"},
		},
	})
}
func (d *codexSearchHomeDispatcher) RPopAuthWithPolicy(ctx context.Context, model, sessionID string, headers http.Header, count int, _ string) ([]byte, error) {
	return d.RPopAuth(ctx, model, sessionID, headers, count)
}
func (*codexSearchHomeDispatcher) AbortAmbiguousDispatch() {}

type errorSearchResponseBody struct {
	payload []byte
}

func (e *errorSearchResponseBody) Read(p []byte) (int, error) {
	if len(e.payload) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, e.payload)
	e.payload = e.payload[n:]
	return n, nil
}
func (*errorSearchResponseBody) Close() error { return nil }
