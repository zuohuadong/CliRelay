package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

type usageCapturePlugin struct {
	records chan cliproxyusage.Record
}

func (p *usageCapturePlugin) HandleUsage(ctx context.Context, record cliproxyusage.Record) {
	select {
	case p.records <- record:
	default:
	}
}

func TestCodexExecutorExecuteImageGeneration(t *testing.T) {
	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+XgnUAAAAASUVORK5CYII="
	pngBytes := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x04, 0x00, 0x00, 0x00, 0xB5, 0x1C, 0x0C,
		0x02, 0x00, 0x00, 0x00, 0x0B, 0x49, 0x44, 0x41,
		0x54, 0x78, 0xDA, 0x63, 0xFC, 0xFF, 0x1F, 0x00,
		0x03, 0x03, 0x02, 0x00, 0xEF, 0x97, 0x82, 0x75,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44,
		0xAE, 0x42, 0x60, 0x82,
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"chat-token","arkose":{"required":false},"proofofwork":{"required":false}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/conversation/init":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation/prepare":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"conduit_token":"conduit-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-1\"}\n\n"))
			_, _ = w.Write([]byte("data: {\"message\":{\"metadata\":{\"dalle\":{\"prompt\":\"revised fox prompt\"}}},\"asset_pointer\":\"file-service://file-123\"}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/backend-api/files/file-123/download":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"download_url":"` + server.URL + `/download/image.png"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/download/image.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	originalBaseURL := codexImageChatGPTBaseURL
	codexImageChatGPTBaseURL = server.URL
	defer func() {
		codexImageChatGPTBaseURL = originalBaseURL
	}()

	usagePlugin := &usageCapturePlugin{records: make(chan cliproxyusage.Record, 8)}
	cliproxyusage.RegisterPlugin(usagePlugin)

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token",
			"account_id":   "account-1",
		},
	}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"draw a fox"}`),
		Format:  sdktranslator.FromString("openai"),
	}, cliproxyexecutor.Options{
		Alt:          "images/generations",
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var payload struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON       string `json:"b64_json"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("data length = %d, want 1", len(payload.Data))
	}
	if payload.Data[0].B64JSON != pngBase64 {
		t.Fatalf("b64_json = %q", payload.Data[0].B64JSON)
	}
	if payload.Data[0].RevisedPrompt != "revised fox prompt" {
		t.Fatalf("revised_prompt = %q", payload.Data[0].RevisedPrompt)
	}
	if !strings.Contains(resp.Headers.Get("Content-Type"), "text/event-stream") && len(resp.Headers) == 0 {
		t.Fatalf("expected upstream headers to be preserved")
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case record := <-usagePlugin.records:
			if record.Model != "gpt-image-2" {
				continue
			}
			if record.Failed {
				t.Fatalf("expected successful usage record, got failed=true")
			}
			if !strings.Contains(record.InputContent, "draw a fox") {
				t.Fatalf("input content = %q, want prompt", record.InputContent)
			}
			if !strings.Contains(record.OutputContent, pngBase64) {
				t.Fatalf("output content = %q, want response payload", record.OutputContent)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for image generation usage record")
		}
	}
}

func TestCodexExecutorExecuteImageGenerationRunsMultipleImagesSequentially(t *testing.T) {
	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+XgnUAAAAASUVORK5CYII="
	pngBytes := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x04, 0x00, 0x00, 0x00, 0xB5, 0x1C, 0x0C,
		0x02, 0x00, 0x00, 0x00, 0x0B, 0x49, 0x44, 0x41,
		0x54, 0x78, 0xDA, 0x63, 0xFC, 0xFF, 0x1F, 0x00,
		0x03, 0x03, 0x02, 0x00, 0xEF, 0x97, 0x82, 0x75,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44,
		0xAE, 0x42, 0x60, 0x82,
	}

	var conversationCount atomic.Int32
	var inFlight atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"chat-token","arkose":{"required":false},"proofofwork":{"required":false}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/conversation/init":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation/prepare":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"conduit_token":"conduit-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation":
			count := conversationCount.Add(1)
			if current := inFlight.Add(1); current != 1 {
				t.Fatalf("image generations should run sequentially, got %d in-flight requests", current)
			}
			defer inFlight.Add(-1)
			time.Sleep(20 * time.Millisecond)
			w.Header().Set("Content-Type", "text/event-stream")
			fileID := "file-" + strconv.Itoa(int(count))
			_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-" + fileID + "\"}\n\n"))
			_, _ = w.Write([]byte("data: {\"asset_pointer\":\"file-service://" + fileID + "\"}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/backend-api/files/file-") && strings.HasSuffix(r.URL.Path, "/download"):
			fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/backend-api/files/"), "/download")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"download_url":"` + server.URL + `/download/` + fileID + `.png"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/download/file-"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	originalBaseURL := codexImageChatGPTBaseURL
	codexImageChatGPTBaseURL = server.URL
	defer func() {
		codexImageChatGPTBaseURL = originalBaseURL
	}()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token",
			"account_id":   "account-1",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"draw a fox","n":4}`),
		Format:  sdktranslator.FromString("openai"),
	}, cliproxyexecutor.Options{
		Alt:          "images/generations",
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var payload struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	if len(payload.Data) != 4 {
		t.Fatalf("data length = %d, want 4", len(payload.Data))
	}
	for i, item := range payload.Data {
		if item.B64JSON != pngBase64 {
			t.Fatalf("data[%d].b64_json = %q", i, item.B64JSON)
		}
	}
}

func TestCodexExecutorExecuteImageGenerationSkipsPollingWhenStreamAlreadyHasInlineImage(t *testing.T) {
	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+XgnUAAAAASUVORK5CYII="

	var conversationPolls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"chat-token","arkose":{"required":false},"proofofwork":{"required":false}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/conversation/init":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation/prepare":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"conduit_token":"conduit-token"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-1\"}\n\n"))
			_, _ = w.Write([]byte("data: {\"message\":{\"metadata\":{\"dalle\":{\"prompt\":\"inline fox prompt\"}}},\"b64_json\":\"" + pngBase64 + "\"}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/backend-api/conversation/conv-1":
			conversationPolls.Add(1)
			t.Fatalf("unexpected conversation poll for inline image stream")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	originalBaseURL := codexImageChatGPTBaseURL
	codexImageChatGPTBaseURL = server.URL
	defer func() {
		codexImageChatGPTBaseURL = originalBaseURL
	}()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token",
			"account_id":   "account-1",
		},
	}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"draw a fox"}`),
		Format:  sdktranslator.FromString("openai"),
	}, cliproxyexecutor.Options{
		Alt:          "images/generations",
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var payload struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("data length = %d, want 1", len(payload.Data))
	}
	if payload.Data[0].B64JSON != pngBase64 {
		t.Fatalf("b64_json = %q, want %q", payload.Data[0].B64JSON, pngBase64)
	}
	if payload.Data[0].RevisedPrompt != "inline fox prompt" {
		t.Fatalf("revised_prompt = %q, want inline fox prompt", payload.Data[0].RevisedPrompt)
	}
	if conversationPolls.Load() != 0 {
		t.Fatalf("conversation polls = %d, want 0", conversationPolls.Load())
	}
}

func TestCodexExecutorRejectsImageEditsWhileDisabled(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token",
		},
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"turn it green","image_files":[{"file_name":"icon.png","content_type":"image/png","data_base64":"aGVsbG8="}]}`),
		Format:  sdktranslator.FromString("openai"),
	}, cliproxyexecutor.Options{
		Alt:          "images/generations",
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want disabled image edits error")
	}
	if !strings.Contains(err.Error(), "image edits are temporarily disabled") {
		t.Fatalf("error = %v, want disabled image edits message", err)
	}
	statusErr, ok := err.(cliproxyexecutor.StatusError)
	if !ok {
		t.Fatalf("error type = %T, want StatusError", err)
	}
	if statusErr.StatusCode() != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", statusErr.StatusCode(), http.StatusNotImplemented)
	}
}

func TestUsageReporterTrackFailureStoresErrorContent(t *testing.T) {
	usagePlugin := &usageCapturePlugin{records: make(chan cliproxyusage.Record, 8)}
	cliproxyusage.RegisterPlugin(usagePlugin)

	auth := &cliproxyauth.Auth{
		ID:       "codex-auth-failure",
		Provider: "codex",
		Status:   cliproxyauth.StatusActive,
	}
	reporter := newUsageReporter(context.Background(), "codex", "gpt-image-2", auth)
	reporter.setInputContent(`{"model":"gpt-image-2","prompt":"draw a fox"}`)
	errValue := fmt.Errorf("openai image conversation returned no downloadable images")

	reporter.trackFailure(context.Background(), &errValue)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case record := <-usagePlugin.records:
			if record.Model != "gpt-image-2" {
				continue
			}
			if !record.Failed {
				t.Fatalf("record.Failed = false, want true")
			}
			if !strings.Contains(record.InputContent, "draw a fox") {
				t.Fatalf("InputContent = %q, want request payload", record.InputContent)
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(record.OutputContent), &body); err != nil {
				t.Fatalf("OutputContent = %q, want structured json error: %v", record.OutputContent, err)
			}
			if body.Error.Type != "upstream_error" {
				t.Fatalf("error.type = %q, want upstream_error", body.Error.Type)
			}
			if !strings.Contains(body.Error.Message, "no downloadable images") {
				t.Fatalf("error.message = %q, want failure message", body.Error.Message)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for failure usage record")
		}
	}
}

func TestUsageReporterTrackFailureStoresOfficialUpstreamBody(t *testing.T) {
	usagePlugin := &usageCapturePlugin{records: make(chan cliproxyusage.Record, 8)}
	cliproxyusage.RegisterPlugin(usagePlugin)

	auth := &cliproxyauth.Auth{
		ID:       "codex-auth-official-failure",
		Provider: "codex",
		Status:   cliproxyauth.StatusActive,
	}
	reporter := newUsageReporter(context.Background(), "codex", "gpt-image-2", auth)
	reporter.setInputContent(`{"model":"gpt-image-2","prompt":"draw a fox"}`)
	errValue := error(statusErr{
		code:         http.StatusTooManyRequests,
		msg:          "rate limit exceeded",
		upstreamBody: []byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit_error","param":null,"code":"rate_limit"}}`),
	})

	reporter.trackFailure(context.Background(), &errValue)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case record := <-usagePlugin.records:
			if record.Model != "gpt-image-2" {
				continue
			}
			var body struct {
				Error struct {
					Message  string `json:"message"`
					Type     string `json:"type"`
					Upstream struct {
						Error struct {
							Message string `json:"message"`
							Type    string `json:"type"`
							Code    string `json:"code"`
						} `json:"error"`
					} `json:"upstream"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(record.OutputContent), &body); err != nil {
				t.Fatalf("OutputContent = %q, want structured json error: %v", record.OutputContent, err)
			}
			if body.Error.Type != "upstream_error" {
				t.Fatalf("error.type = %q, want upstream_error", body.Error.Type)
			}
			if body.Error.Upstream.Error.Type != "rate_limit_error" {
				t.Fatalf("upstream.error.type = %q, want rate_limit_error", body.Error.Upstream.Error.Type)
			}
			if body.Error.Upstream.Error.Code != "rate_limit" {
				t.Fatalf("upstream.error.code = %q, want rate_limit", body.Error.Upstream.Error.Code)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for failure usage record")
		}
	}
}

func TestWrapCodexImagePhaseErrorPrefixesPhase(t *testing.T) {
	err := wrapCodexImagePhaseError("conversation poll", context.Canceled)
	if err == nil {
		t.Fatal("wrapCodexImagePhaseError() error = nil")
	}
	if !strings.Contains(err.Error(), "conversation poll") {
		t.Fatalf("error = %q, want phase prefix", err.Error())
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("error = %q, want original error text", err.Error())
	}
}

func TestPollCodexImageConversationWaitsUntilPointersAppear(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/backend-api/conversation/conv-1" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		count := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			_, _ = w.Write([]byte(`{"mapping":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"mapping": {
				"tool-message": {
					"message": {
						"author": {"role": "tool"},
						"create_time": 2,
						"metadata": {"async_task_type": "image_gen"},
						"content": {
							"content_type": "multimodal_text",
							"parts": [{"asset_pointer":"file-service://generated-file"}]
						}
					}
				}
			}
		}`))
	}))
	defer server.Close()

	originalBaseURL := codexImageChatGPTBaseURL
	originalTimeout := codexImagePollTimeout
	originalInterval := codexImagePollInterval
	codexImageChatGPTBaseURL = server.URL
	codexImagePollTimeout = 500 * time.Millisecond
	codexImagePollInterval = 10 * time.Millisecond
	defer func() {
		codexImageChatGPTBaseURL = originalBaseURL
		codexImagePollTimeout = originalTimeout
		codexImagePollInterval = originalInterval
	}()

	pointers, err := pollCodexImageConversation(context.Background(), server.Client(), nil, "conv-1")
	if err != nil {
		t.Fatalf("pollCodexImageConversation() error = %v", err)
	}
	if len(pointers) != 1 {
		t.Fatalf("pointers length = %d, want 1", len(pointers))
	}
	if pointers[0].Pointer != "file-service://generated-file" {
		t.Fatalf("pointer = %q, want generated file pointer", pointers[0].Pointer)
	}
	if requests.Load() < 2 {
		t.Fatalf("requests = %d, want at least 2", requests.Load())
	}
}

func TestParseCodexImageRequestAcceptsExtendedGenerationOptions(t *testing.T) {
	parsed, err := parseCodexImageRequest([]byte(`{
		"model":"gpt-image-2",
		"prompt":"draw a fox",
		"size":"1792x1024",
		"quality":"high",
		"n":3
	}`))
	if err != nil {
		t.Fatalf("parseCodexImageRequest() error = %v", err)
	}
	if parsed.Model != "gpt-image-2" {
		t.Fatalf("model = %q, want gpt-image-2", parsed.Model)
	}
	if parsed.Prompt != "draw a fox" {
		t.Fatalf("prompt = %q, want draw a fox", parsed.Prompt)
	}
	if parsed.Size != "1792x1024" {
		t.Fatalf("size = %q, want 1792x1024", parsed.Size)
	}
	if parsed.Quality != "high" {
		t.Fatalf("quality = %q, want high", parsed.Quality)
	}
	if parsed.N != 3 {
		t.Fatalf("n = %d, want 3", parsed.N)
	}

	for _, size := range []string{"2560x1440", "2160x3840"} {
		parsed, err := parseCodexImageRequest([]byte(`{
			"model":"gpt-image-2",
			"prompt":"draw a fox",
			"size":"` + size + `"
		}`))
		if err != nil {
			t.Fatalf("parseCodexImageRequest(size=%s) error = %v", size, err)
		}
		if parsed.Size != size {
			t.Fatalf("size = %q, want %q", parsed.Size, size)
		}
	}
}

func TestParseCodexImageRequestAcceptsImageEditsPayload(t *testing.T) {
	parsed, err := parseCodexImageRequest([]byte(`{
		"model":"gpt-image-2",
		"prompt":"turn this into a blue icon",
		"image_files":[
			{
				"file_name":"icon.png",
				"content_type":"image/png",
				"data_base64":"aGVsbG8="
			}
		]
	}`))
	if err != nil {
		t.Fatalf("parseCodexImageRequest() error = %v", err)
	}
	if len(parsed.Uploads) != 1 {
		t.Fatalf("uploads length = %d, want 1", len(parsed.Uploads))
	}
	if parsed.Uploads[0].FileName != "icon.png" {
		t.Fatalf("file name = %q, want icon.png", parsed.Uploads[0].FileName)
	}
	if parsed.Uploads[0].ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", parsed.Uploads[0].ContentType)
	}
	if string(parsed.Uploads[0].Data) != "hello" {
		t.Fatalf("upload data = %q, want hello", string(parsed.Uploads[0].Data))
	}
}

func TestParseCodexImageRequestRejectsMoreThanFiveImageEdits(t *testing.T) {
	_, err := parseCodexImageRequest([]byte(`{
		"model":"gpt-image-2",
		"prompt":"turn this into a blue icon",
		"image_files":[
			{"file_name":"1.png","content_type":"image/png","data_base64":"aGVsbG8="},
			{"file_name":"2.png","content_type":"image/png","data_base64":"aGVsbG8="},
			{"file_name":"3.png","content_type":"image/png","data_base64":"aGVsbG8="},
			{"file_name":"4.png","content_type":"image/png","data_base64":"aGVsbG8="},
			{"file_name":"5.png","content_type":"image/png","data_base64":"aGVsbG8="},
			{"file_name":"6.png","content_type":"image/png","data_base64":"aGVsbG8="}
		]
	}`))
	if err == nil {
		t.Fatal("parseCodexImageRequest() error = nil, want max image count validation error")
	}
	if !strings.Contains(err.Error(), "at most 5 images") {
		t.Fatalf("error = %v, want max image count validation error", err)
	}
}

func TestCollectCodexImagePointersRecognizesDirectAssets(t *testing.T) {
	items := collectCodexImagePointers([]byte(`{
		"revised_prompt": "cat astronaut",
		"parts": [
			{"b64_json":"QUJD"},
			{"download_url":"https://files.example.com/image.png?sig=1"},
			{"asset_pointer":"file-service://file_123"}
		]
	}`))

	if len(items) != 3 {
		t.Fatalf("items length = %d, want 3: %#v", len(items), items)
	}
	var sawBase64, sawURL, sawPointer bool
	for _, item := range items {
		if item.B64JSON == "QUJD" {
			sawBase64 = true
			if item.Prompt != "cat astronaut" {
				t.Fatalf("base64 prompt = %q, want cat astronaut", item.Prompt)
			}
		}
		if item.DownloadURL == "https://files.example.com/image.png?sig=1" {
			sawURL = true
		}
		if item.Pointer == "file-service://file_123" {
			sawPointer = true
		}
	}
	if !sawBase64 || !sawURL || !sawPointer {
		t.Fatalf("items = %#v, want base64, download URL, and pointer assets", items)
	}
}

func TestResolveCodexImageBytesPrefersInlineBase64(t *testing.T) {
	data, err := resolveCodexImageBytes(context.Background(), nil, nil, "", codexImagePointer{
		B64JSON: "data:image/png;base64,QUJD",
	})
	if err != nil {
		t.Fatalf("resolveCodexImageBytes() error = %v", err)
	}
	if string(data) != "ABC" {
		t.Fatalf("data = %q, want ABC", string(data))
	}
}

func TestExtractCodexImageToolMessagesPrefersToolAssets(t *testing.T) {
	mapping := map[string]any{
		"user-message": map[string]any{
			"message": map[string]any{
				"author": map[string]any{"role": "user"},
				"content": map[string]any{
					"content_type": "multimodal_text",
					"parts": []any{
						map[string]any{"asset_pointer": "file-service://input-file"},
					},
				},
				"metadata": map[string]any{},
			},
		},
		"tool-message": map[string]any{
			"message": map[string]any{
				"author":      map[string]any{"role": "tool"},
				"create_time": 2.0,
				"metadata": map[string]any{
					"async_task_type": "image_gen",
					"image_gen_title": "red circle icon",
				},
				"content": map[string]any{
					"content_type": "multimodal_text",
					"parts": []any{
						map[string]any{"b64_json": "QUJD"},
					},
				},
			},
		},
	}

	messages := extractCodexImageToolMessages(mapping)

	if len(messages) != 1 {
		t.Fatalf("tool messages length = %d, want 1", len(messages))
	}
	if len(messages[0].Pointers) != 1 {
		t.Fatalf("tool pointers length = %d, want 1", len(messages[0].Pointers))
	}
	if messages[0].Pointers[0].B64JSON != "QUJD" {
		t.Fatalf("tool pointer = %#v, want inline base64 asset", messages[0].Pointers[0])
	}
	if messages[0].Pointers[0].Prompt != "red circle icon" {
		t.Fatalf("tool prompt = %q, want red circle icon", messages[0].Pointers[0].Prompt)
	}
}

func TestBuildCodexImageOpenAIResponseKeepsSourceImageWhenReturned(t *testing.T) {
	payload, err := buildCodexImageOpenAIResponse(
		context.Background(),
		nil,
		nil,
		"",
		[]codexImagePointer{{B64JSON: "QUJD"}},
	)
	if err != nil {
		t.Fatalf("buildCodexImageOpenAIResponse() error = %v", err)
	}
	var parsed struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	if len(parsed.Data) != 1 || parsed.Data[0].B64JSON != "QUJD" {
		t.Fatalf("payload = %s, want returned base64 image to be preserved", string(payload))
	}
}

func TestCollectCodexImagePollPointersKeepsUploadedSourcePointers(t *testing.T) {
	body := []byte(`{
		"mapping": {
			"user-message": {
				"message": {
					"author": {"role": "user"},
					"content": {
						"content_type": "multimodal_text",
						"parts": [{"asset_pointer":"file-service://input-file"}]
					},
					"metadata": {}
				}
			},
			"tool-message": {
				"message": {
					"author": {"role": "tool"},
					"create_time": 2,
					"metadata": {
						"async_task_type": "image_gen",
						"image_gen_title": "green icon"
					},
					"content": {
						"content_type": "multimodal_text",
						"parts": [{"asset_pointer":"file-service://input-file"}]
					}
				}
			}
		}
	}`)

	items := collectCodexImagePollPointers(body)

	if len(items) != 1 {
		t.Fatalf("items = %#v, want uploaded source pointer to be preserved", items)
	}
	if items[0].Pointer != "file-service://input-file" {
		t.Fatalf("pointer = %#v, want uploaded source pointer", items[0])
	}
}

func TestCollectCodexImagePollPointersKeepsGeneratedToolAssets(t *testing.T) {
	body := []byte(`{
		"mapping": {
			"tool-message": {
				"message": {
					"author": {"role": "tool"},
					"create_time": 2,
					"metadata": {
						"async_task_type": "image_gen",
						"image_gen_title": "green icon"
					},
					"content": {
						"content_type": "multimodal_text",
						"parts": [{"asset_pointer":"file-service://generated-file"}]
					}
				}
			}
		}
	}`)

	items := collectCodexImagePollPointers(body)

	if len(items) != 1 {
		t.Fatalf("items length = %d, want 1", len(items))
	}
	if items[0].Pointer != "file-service://generated-file" {
		t.Fatalf("pointer = %#v, want generated file-service pointer", items[0])
	}
}

func TestBuildCodexImagePromptForEditsForcesNewOutput(t *testing.T) {
	prompt := buildCodexImagePrompt(&codexImageRequest{
		Prompt: "把这张红色图标改成绿色图标",
		Uploads: []codexImageUpload{
			{FileName: "source.png", Data: []byte("abc")},
		},
	}, 0)

	if !strings.Contains(prompt, "Do not return the original uploaded image") {
		t.Fatalf("prompt = %q, want explicit instruction to avoid returning the uploaded image", prompt)
	}
}
