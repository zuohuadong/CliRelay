package executor

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func validCodexReasoningEncryptedContentForTest() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func newCodexSignatureTestAuth(serverURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": serverURL,
		"api_key":  "test",
	}}
}

func TestCodexExecutorDropsUnreplayableReasoningItemsFromFinalRequest(t *testing.T) {
	validEncryptedContent := validCodexReasoningEncryptedContentForTest()
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), newCodexSignatureTestAuth(server.URL), cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[` +
			`{"id":"rs_bad","type":"reasoning","encrypted_content":"gAAAAABqFTIa\u2026abc","summary":[]},` +
			`{"id":"rs_non_string","type":"reasoning","encrypted_content":123,"summary":[]},` +
			`{"id":"rs_good","type":"reasoning","encrypted_content":"` + validEncryptedContent + `","summary":[]},` +
			`{"role":"user","content":"hello","encrypted_content":"leave-message-alone"}` +
			`]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	input := gjson.GetBytes(gotBody, "input").Array()
	if len(input) != 2 {
		t.Fatalf("input length = %d, want 2 after dropping unreplayable reasoning items; body=%s", len(input), string(gotBody))
	}
	if got := input[0].Get("id").String(); got != "rs_good" {
		t.Fatalf("input[0].id = %q, want rs_good; body=%s", got, string(gotBody))
	}
	if got := input[0].Get("encrypted_content").String(); got != validEncryptedContent {
		t.Fatalf("valid reasoning encrypted_content = %q, want preserved", got)
	}
	if got := input[1].Get("encrypted_content").String(); got != "leave-message-alone" {
		t.Fatalf("non-reasoning encrypted_content = %q, want untouched", got)
	}
}

func TestCodexExecutorExecuteStreamDropsUnreplayableReasoningItemsFromFinalRequest(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	result, err := executor.ExecuteStream(context.Background(), newCodexSignatureTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","stream":true,"input":[{"id":"rs_bad","type":"reasoning","encrypted_content":"bad","summary":[]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}
	if input := gjson.GetBytes(gotBody, "input").Array(); len(input) != 0 {
		t.Fatalf("input length = %d, want 0 after dropping unreplayable stream reasoning item; body=%s", len(input), string(gotBody))
	}
}

func TestCodexExecutorExecuteStreamDropsUnreplayableSyntheticReasoningItem(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	result, err := executor.ExecuteStream(context.Background(), newCodexSignatureTestAuth(server.URL), cliproxyexecutor.Request{
		Model: "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","stream":true,"store":false,"input":[` +
			`{"id":"rs_resp_foreign_0","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":"foreign reasoning"}]},` +
			`{"role":"user","content":"continue"}` +
			`]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}

	input := gjson.GetBytes(gotBody, "input").Array()
	if len(input) != 1 {
		t.Fatalf("input length = %d, want 1 after dropping synthetic reasoning item; body=%s", len(input), string(gotBody))
	}
	if got := input[0].Get("role").String(); got != "user" {
		t.Fatalf("input[0].role = %q, want user; body=%s", got, string(gotBody))
	}
	if got := input[0].Get("content").String(); got != "continue" {
		t.Fatalf("input[0].content = %q, want continue; body=%s", got, string(gotBody))
	}
}

func TestCodexExecutorCompactDropsUnreplayableReasoningItemsFromFinalRequest(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), newCodexSignatureTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[{"id":"rs_bad","type":"reasoning","encrypted_content":"bad","summary":[]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute compact error: %v", err)
	}
	if input := gjson.GetBytes(gotBody, "input").Array(); len(input) != 0 {
		t.Fatalf("input length = %d, want 0 after dropping unreplayable compact reasoning item; body=%s", len(input), string(gotBody))
	}
}
