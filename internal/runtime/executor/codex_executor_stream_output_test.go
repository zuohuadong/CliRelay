package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecute_NonEmptyCompletionOutputHydratesMissingItemID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_123","type":"function_call","call_id":"call_123","name":"weather","arguments":"{}"},"output_index":0}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_done_existing","type":"function_call","call_id":"call_existing","name":"other","arguments":"{}"},"output_index":1}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[{"id":null,"type":"function_call","call_id":"call_123","name":"weather-terminal","arguments":"{}"},{"id":"fc_existing","type":"function_call","call_id":"call_existing","name":"preserved","arguments":"{}"}]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"What is the weather?"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(resp.Payload, "output.0.id").String(); got != "fc_123" {
		t.Fatalf("output[0].id = %q, want %q; payload=%s", got, "fc_123", resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.name").String(); got != "weather-terminal" {
		t.Fatalf("output[0].name = %q, want terminal value; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "output.1.id").String(); got != "fc_existing" {
		t.Fatalf("output[1].id = %q, want existing value; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorExecute_EmptyStreamCompletionOutputUsesOutputItemDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":28,\"total_tokens\":36}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"Say ok"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	gotContent := gjson.GetBytes(resp.Payload, "choices.0.message.content").String()
	if gotContent != "ok" {
		t.Fatalf("choices.0.message.content = %q, want %q; payload=%s", gotContent, "ok", string(resp.Payload))
	}
}

func TestCodexExecutorExecuteSurfacesTerminalStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected terminal stream error, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertCodexErrorCode(t, err.Error(), "invalid_request_error", "context_too_large")
	if !strings.Contains(err.Error(), "Your input exceeds the context window") {
		t.Fatalf("error message missing upstream context text: %v", err)
	}
}

func TestCodexExecutorExecuteIncompleteResponseIsSuccessful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.incomplete","response":{"id":"resp_1","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want %q; payload=%s", got, "max_tokens", resp.Payload)
	}
}

func TestCodexExecutorExecuteExplicitTerminalFailureIsNotRequestScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input."}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected explicit terminal failure, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertNotRequestScopedTestError(t, err)
}

func TestCodexExecutorExecuteMissingCompletionIsNotRequestScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\"}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected missing-completion error, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, err)
	}
	// 非流式断流时客户端尚未收到任何内容，轮换凭证重试是安全的。
	assertNotRequestScopedTestError(t, err)
}

func TestCodexExecutorExecuteStreamMissingCompletionIsNotRequestScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\"}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("expected missing-completion stream error, got nil")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, streamErr)
	}
	// 尚未向下游输出任何 payload 的断流可以安全轮换凭证重试。
	assertNotRequestScopedTestError(t, streamErr)
}

func TestCodexExecutorExecuteStreamIncompleteAfterProgressIsRequestScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\"}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("expected incomplete stream error, got nil")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, streamErr)
	}
	// 实质输出事件已经下发，重试会重复内容，必须停止凭证回退。
	assertRequestScopedTestError(t, streamErr)
}

func TestCodexExecutorExecuteStreamExplicitTerminalFailureIsNotSuccessful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\"}}\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input."}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("expected explicit terminal stream error, got nil")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, streamErr)
	}
	assertNotRequestScopedTestError(t, streamErr)
}

func TestCodexAutoExecutorHTTPFallbackForwardsSequentialCutoffReasoningSummaryDelivery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		if gjson.GetBytes(body, "stream_options.include_usage").Exists() {
			t.Errorf("unsupported stream option was forwarded: %s", body)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		if delivery := gjson.GetBytes(body, "stream_options.reasoning_summary_delivery").String(); delivery == "sequential_cutoff" {
			_, _ = w.Write([]byte(`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","summary_index":0,"text":"Checking"}` + "\n\n"))
		} else {
			_, _ = w.Write([]byte(`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":0,"delta":"Checking"}` + "\n\n"))
		}
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexAutoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello","reasoning":{"summary":"detailed"},"stream_options":{"reasoning_summary_delivery":"sequential_cutoff","include_usage":true}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Stream:         true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var output bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), `"type":"response.reasoning_summary_text.done"`) {
		t.Fatalf("missing sequential-cutoff summary event; output=%s", output.String())
	}
}

func TestCodexExecutorTransportFailureBeforeTerminalIsNotRequestScoped(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
	}{
		{name: "non-streaming"},
		{name: "streaming", stream: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			created := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\"}}\n\n")
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}},
					Body:       io.NopCloser(io.MultiReader(bytes.NewReader(created), unexpectedEOFReader{})),
					Request:    req,
				}, nil
			}))

			executor := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"base_url": "http://codex.test",
				"api_key":  "test",
			}}
			req := cliproxyexecutor.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: tc.stream}

			var terminalErr error
			if tc.stream {
				result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream error: %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						terminalErr = chunk.Err
					}
				}
			} else {
				_, terminalErr = executor.Execute(ctx, auth, req, opts)
			}
			if terminalErr == nil {
				t.Fatal("expected transport failure before terminal event")
			}
			if got := statusCodeFromTestError(t, terminalErr); got != http.StatusRequestTimeout {
				t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, terminalErr)
			}
			// created 之后立即断流，客户端尚未收到任何实质输出，可安全轮换凭证重试。
			assertNotRequestScopedTestError(t, terminalErr)
		})
	}
}

func TestCodexExecutorExecuteIgnoresTransportErrorAfterCompletion(t *testing.T) {
	completed := []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(io.MultiReader(bytes.NewReader(completed), unexpectedEOFReader{})),
			Request:    req,
		}, nil
	}))

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": "http://codex.test",
		"api_key":  "test",
	}}

	resp, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("unexpected error after response.completed: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "id").String(); got != "resp_1" {
		t.Fatalf("response id = %q, want resp_1; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorExecuteStreamIgnoresTransportErrorAfterCompletion(t *testing.T) {
	completed := []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(io.MultiReader(bytes.NewReader(completed), unexpectedEOFReader{})),
			Request:    req,
		}, nil
	}))

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": "http://codex.test",
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr != nil {
		t.Fatalf("unexpected error after response.completed: %v", streamErr)
	}
}

func TestCodexExecutorExecuteStreamSurfacesTerminalStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			break
		}
	}
	if streamErr == nil {
		t.Fatal("missing stream terminal error")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, streamErr)
	}
	assertCodexErrorCode(t, streamErr.Error(), "invalid_request_error", "context_too_large")
}

func TestCodexExecutorExecuteStreamRejectsEmptyCompletedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1775555723,"status":"completed","model":"gpt-5.4-mini-2026-03-17","output":[],"usage":{"input_tokens":8,"output_tokens":0,"total_tokens":8}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream setup error: %v", err)
	}

	var got strings.Builder
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			break
		}
		got.Write(chunk.Payload)
	}
	if got.String() != "" {
		t.Fatalf("expected no payload before empty response error, got %q", got.String())
	}
	if streamErr == nil {
		t.Fatal("expected empty completed response error")
	}
	if gotStatus := statusCodeFromTestError(t, streamErr); gotStatus != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d; err=%v", gotStatus, http.StatusBadGateway, streamErr)
	}
	if !strings.Contains(streamErr.Error(), "empty stream response") {
		t.Fatalf("stream error = %v, want empty stream response", streamErr)
	}
}

func TestCodexExecutorExecuteSurfacesResponseFailedRateLimitError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached."}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected terminal stream error, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusTooManyRequests, err)
	}
	assertCodexErrorCode(t, err.Error(), "rate_limit_error", "rate_limit_exceeded")
	if !strings.Contains(err.Error(), "Rate limit reached") {
		t.Fatalf("error message missing upstream rate limit text: %v", err)
	}
}

func TestCodexExecutorExecuteTreatsResponseIncompleteAsTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.incomplete\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(resp.Payload, "status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete_details.reason = %q, want max_output_tokens; payload=%s", got, resp.Payload)
	}
}

func TestCodexTerminalStreamContextLengthErrFromResponseFailed(t *testing.T) {
	err, ok := codexTerminalStreamContextLengthErr([]byte(`{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}`))
	if !ok {
		t.Fatal("expected context length terminal error")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertCodexErrorCode(t, err.Error(), "invalid_request_error", "context_too_large")
}

func TestCodexTerminalStreamContextLengthErrFromTopLevelError(t *testing.T) {
	err, ok := codexTerminalStreamContextLengthErr([]byte(`{"type":"error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","sequence_number":2}`))
	if !ok {
		t.Fatal("expected top-level context length terminal error")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertCodexErrorCode(t, err.Error(), "invalid_request_error", "context_too_large")
	if !strings.Contains(err.Error(), "Your input exceeds the context window") {
		t.Fatalf("error message missing upstream context text: %v", err)
	}
}

func TestCodexTerminalStreamContextLengthErrIgnoresOtherTerminalErrors(t *testing.T) {
	_, ok := codexTerminalStreamContextLengthErr([]byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached."}}`))
	if ok {
		t.Fatal("rate limit terminal error should not be handled by context length fix")
	}
}

func TestCodexTerminalStreamErrSurfacesRateLimitTerminalErrors(t *testing.T) {
	err, _, ok := codexTerminalStreamErr([]byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached."}}`))
	if !ok {
		t.Fatal("rate limit terminal error was not handled")
	}
	if got := err.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestCodexTerminalFailureErrClassifiesStatus(t *testing.T) {
	tests := []struct {
		name       string
		event      string
		wantStatus int
	}{
		{
			name:       "invalid request",
			event:      `{"type":"error","error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input."}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "cyber policy",
			event:      `{"type":"error","error":{"type":"invalid_request","code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk."}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "authentication",
			event:      `{"type":"response.failed","response":{"error":{"type":"authentication_error","code":"invalid_api_key","message":"Invalid token."}}}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "rate limit",
			event:      `{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached."}}`,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "unknown upstream failure",
			event:      `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"unknown","message":"Upstream failed."}}}`,
			wantStatus: http.StatusBadGateway,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			streamErr, _, ok := codexTerminalFailureErr([]byte(tc.event))
			if !ok {
				t.Fatal("expected terminal failure to be handled")
			}
			if got := streamErr.StatusCode(); got != tc.wantStatus {
				t.Fatalf("status code = %d, want %d; err=%v", got, tc.wantStatus, streamErr)
			}
		})
	}
}

func TestCodexTerminalStreamErrHandlesUsageLimitErrorEvent(t *testing.T) {
	streamErr, _, ok := codexTerminalStreamErr([]byte(`{"type":"error","error":{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":300}}`))
	if !ok {
		t.Fatal("expected usage_limit_reached terminal error to be handled")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	retryAfter := streamErr.RetryAfter()
	if retryAfter == nil {
		t.Fatal("expected retryAfter from usage_limit_reached terminal error")
	}
	if *retryAfter != 300*time.Second {
		t.Fatalf("retryAfter = %v, want %v", *retryAfter, 300*time.Second)
	}
}

func TestCodexTerminalStreamErrHandlesUsageLimitResponseFailed(t *testing.T) {
	streamErr, _, ok := codexTerminalStreamErr([]byte(`{"type":"response.failed","response":{"error":{"type":"usage_limit_reached","message":"usage limit reached","resets_in_seconds":60}}}`))
	if !ok {
		t.Fatal("expected usage_limit_reached response.failed terminal error to be handled")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if streamErr.RetryAfter() == nil {
		t.Fatal("expected retryAfter from usage_limit_reached response.failed terminal error")
	}
}

func statusCodeFromTestError(t *testing.T, err error) int {
	t.Helper()

	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose StatusCode(): %v", err, err)
	}
	return statusErr.StatusCode()
}

func assertRequestScopedTestError(t *testing.T, err error) {
	t.Helper()

	requestErr, ok := err.(interface{ IsRequestScoped() bool })
	if !ok {
		t.Fatalf("error %T does not expose IsRequestScoped(): %v", err, err)
	}
	if !requestErr.IsRequestScoped() {
		t.Fatalf("error %T is not request-scoped: %v", err, err)
	}
}

func assertNotRequestScopedTestError(t *testing.T, err error) {
	t.Helper()

	requestErr, ok := err.(interface{ IsRequestScoped() bool })
	if ok && requestErr.IsRequestScoped() {
		t.Fatalf("error %T is unexpectedly request-scoped: %v", err, err)
	}
}

type unexpectedEOFReader struct{}

func (unexpectedEOFReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestCodexExecutorExecuteStream_EmptyStreamCompletionOutputUsesOutputItemDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":28,\"total_tokens\":36}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var completed []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		payload := bytes.TrimSpace(chunk.Payload)
		if !bytes.HasPrefix(payload, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(payload[5:])
		if gjson.GetBytes(data, "type").String() == "response.completed" {
			completed = append([]byte(nil), data...)
		}
	}

	if len(completed) == 0 {
		t.Fatal("missing response.completed chunk")
	}

	gotContent := gjson.GetBytes(completed, "response.output.0.content.0.text").String()
	if gotContent != "ok" {
		t.Fatalf("response.output[0].content[0].text = %q, want %q; completed=%s", gotContent, "ok", string(completed))
	}
}

func TestCodexExecutorExecuteStreamIdleTimeout(t *testing.T) {
	// 缩短 idle timeout 以加快测试
	original := codexHTTPStreamIdleTimeout
	codexHTTPStreamIdleTimeout = 200 * time.Millisecond
	defer func() { codexHTTPStreamIdleTimeout = original }()

	// 模拟上游接受连接但不发送任何 SSE 数据
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 阻塞直到客户端断开
		<-r.Context().Done()
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			break
		}
	}
	if streamErr == nil {
		t.Fatal("expected idle timeout error, got nil")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, streamErr)
	}
	if !strings.Contains(streamErr.Error(), "idle timeout") {
		t.Fatalf("error message should contain 'idle timeout': %v", streamErr)
	}
}

func TestCodexExecutorExecuteStreamResponseHeaderTimeout(t *testing.T) {
	roundTripper := codexHeaderTimeoutRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(roundTripper))

	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{ResponseHeaderTimeoutSeconds: 1}})
	started := time.Now()
	_, err := executor.ExecuteStream(ctx, &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": "https://codex-header-timeout.invalid",
		"api_key":  "test",
	}}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err == nil {
		t.Fatal("ExecuteStream() error = nil, want response header timeout")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("ExecuteStream() took %v, want bounded response header wait", elapsed)
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusGatewayTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusGatewayTimeout, err)
	}
	if !strings.Contains(err.Error(), "response header timeout") {
		t.Fatalf("error = %v, want response header timeout detail", err)
	}
}

func TestCodexResponseHeaderTimeoutConfig(t *testing.T) {
	if got := codexResponseHeaderTimeout(&config.Config{}); got != codexDefaultResponseHeaderTimeout {
		t.Fatalf("default timeout = %v, want %v", got, codexDefaultResponseHeaderTimeout)
	}
	if got := codexResponseHeaderTimeout(&config.Config{Codex: config.CodexConfig{ResponseHeaderTimeoutSeconds: 45}}); got != 45*time.Second {
		t.Fatalf("configured timeout = %v, want 45s", got)
	}
	if got := codexResponseHeaderTimeout(&config.Config{Codex: config.CodexConfig{ResponseHeaderTimeoutSeconds: -1}}); got != 0 {
		t.Fatalf("disabled timeout = %v, want 0", got)
	}
}

func TestCodexExecutorExecuteResponseHeaderTimeout(t *testing.T) {
	roundTripper := codexHeaderTimeoutRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(roundTripper))

	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{ResponseHeaderTimeoutSeconds: 1}})
	_, err := executor.Execute(ctx, &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": "https://codex-header-timeout.invalid",
		"api_key":  "test",
	}}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want response header timeout")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusGatewayTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusGatewayTimeout, err)
	}
}

func TestCodexExecutorExecuteStreamResponseHeaderTimeoutDoesNotLimitBody(t *testing.T) {
	payload := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
	roundTripper := codexHeaderTimeoutRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body: &delayedCodexResponseBody{
				ctx:   req.Context(),
				delay: 1200 * time.Millisecond,
				data:  bytes.NewReader(payload),
			},
			Request: req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(roundTripper))

	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{ResponseHeaderTimeoutSeconds: 1}})
	result, err := executor.ExecuteStream(ctx, &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": "https://codex-header-success.invalid",
		"api_key":  "test",
	}}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var streamErr error
	var payloads int
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			break
		}
		if len(chunk.Payload) > 0 {
			payloads++
		}
	}
	if streamErr != nil {
		t.Fatalf("stream error after response headers = %v", streamErr)
	}
	if payloads == 0 {
		t.Fatal("stream produced no payload after delayed response body")
	}
}

type codexHeaderTimeoutRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f codexHeaderTimeoutRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type delayedCodexResponseBody struct {
	ctx   context.Context
	delay time.Duration
	data  *bytes.Reader
	once  bool
}

func (b *delayedCodexResponseBody) Read(p []byte) (int, error) {
	if !b.once {
		b.once = true
		timer := time.NewTimer(b.delay)
		defer timer.Stop()
		select {
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		case <-timer.C:
		}
	}
	return b.data.Read(p)
}

func (b *delayedCodexResponseBody) Close() error { return nil }
