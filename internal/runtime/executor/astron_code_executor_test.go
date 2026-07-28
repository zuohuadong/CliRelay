package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAstronCodeEndpointURLSwapsV2ToV1ForResponses(t *testing.T) {
	got := astronCodeEndpointURL(
		"https://maas-coding-api.cn-huabei-1.xf-yun.com/v2",
		"/responses",
		true,
	)
	want := "https://maas-coding-api.cn-huabei-1.xf-yun.com/v1/responses"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAstronCodeEndpointURLAcceptsFullResponsesEndpoint(t *testing.T) {
	got := astronCodeEndpointURL(
		"https://maas-coding-api.cn-huabei-1.xf-yun.com/v1/responses",
		"/responses",
		true,
	)
	want := "https://maas-coding-api.cn-huabei-1.xf-yun.com/v1/responses"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAstronCodeEndpointURLAcceptsFullResponsesEndpointForCompact(t *testing.T) {
	got := astronCodeEndpointURL(
		"https://maas-coding-api.cn-huabei-1.xf-yun.com/v1/responses",
		"/responses/compact",
		true,
	)
	want := "https://maas-coding-api.cn-huabei-1.xf-yun.com/v1/responses/compact"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAstronCodeEndpointURLKeepsV2ForChat(t *testing.T) {
	got := astronCodeEndpointURL(
		"https://maas-coding-api.cn-huabei-1.xf-yun.com/v2",
		"/chat/completions",
		false,
	)
	want := "https://maas-coding-api.cn-huabei-1.xf-yun.com/v2/chat/completions"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNormalizeAstronToolMessageHistoryDropsInvalidToolCallArguments(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"start"},{"role":"assistant","tool_calls":[{"id":"call-invalid","type":"function","function":{"name":"exec_command","arguments":"not-json"}},{"id":"call-valid","type":"function","function":{"name":"exec_command","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-invalid","content":"invalid result"},{"role":"tool","tool_call_id":"call-valid","content":"valid result"},{"role":"user","content":"next"}]}`)

	sanitized := normalizeAstronToolMessageHistory(payload)

	messages := gjson.GetBytes(sanitized, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages len = %d, want 4: %s", len(messages), sanitized)
	}
	if got := messages[1].Get("tool_calls.0.id").String(); got != "call-valid" {
		t.Fatalf("kept tool call = %q, want call-valid: %s", got, sanitized)
	}
	if got := messages[2].Get("tool_call_id").String(); got != "call-valid" {
		t.Fatalf("kept tool result = %q, want call-valid: %s", got, sanitized)
	}
	if strings.Contains(string(sanitized), "call-invalid") || strings.Contains(string(sanitized), "not-json") {
		t.Fatalf("invalid tool-call arguments leaked through: %s", sanitized)
	}
}

func TestNormalizeAstronToolMessageHistoryKeepsToolCallWithoutArguments(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"start"},{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"exec_command"}}]},{"role":"tool","tool_call_id":"call-1","content":"result"},{"role":"user","content":"next"}]}`)

	sanitized := normalizeAstronToolMessageHistory(payload)

	messages := gjson.GetBytes(sanitized, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages len = %d, want 4: %s", len(messages), sanitized)
	}
	if got := messages[1].Get("tool_calls.0.id").String(); got != "call-1" {
		t.Fatalf("kept tool call = %q, want call-1: %s", got, sanitized)
	}
	if got := messages[2].Get("tool_call_id").String(); got != "call-1" {
		t.Fatalf("kept tool result = %q, want call-1: %s", got, sanitized)
	}
}

func TestAstronCodeEndpointURLSwapsV1ToV2ForChat(t *testing.T) {
	got := astronCodeEndpointURL(
		"https://maas-coding-api.cn-huabei-1.xf-yun.com/v1",
		"/chat/completions",
		false,
	)
	want := "https://maas-coding-api.cn-huabei-1.xf-yun.com/v2/chat/completions"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAstronCodeExecutorExecute429WithoutRetryAfterUsesShortRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	executor := NewAstronCodeExecutor(&config.Config{})
	_, errExecute := executor.Execute(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v2",
		"api_key":  "test",
	}}, cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"retry"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want 429")
	}
	retryable, ok := errExecute.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("RetryAfter() = nil, want short retry; error=%v", errExecute)
	}
	if got := *retryable.RetryAfter(); got != time.Second {
		t.Fatalf("RetryAfter() = %v, want %v", got, time.Second)
	}
}

func TestAstronCodeExecutorExecute429RespectsRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "7")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	executor := NewAstronCodeExecutor(&config.Config{})
	_, errExecute := executor.Execute(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v2",
		"api_key":  "test",
	}}, cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"retry"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want 429")
	}
	retryable, ok := errExecute.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("RetryAfter() = nil, want upstream retry delay; error=%v", errExecute)
	}
	if got := *retryable.RetryAfter(); got != 7*time.Second {
		t.Fatalf("RetryAfter() = %v, want %v", got, 7*time.Second)
	}
}

func TestAstronCodeExecutorExecuteStream429WithoutRetryAfterUsesShortRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	executor := NewAstronCodeExecutor(&config.Config{})
	_, errExecute := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v2",
		"api_key":  "test",
	}}, cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"retry"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errExecute == nil {
		t.Fatal("ExecuteStream() error = nil, want 429")
	}
	retryable, ok := errExecute.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("RetryAfter() = nil, want short retry; error=%v", errExecute)
	}
	if got := *retryable.RetryAfter(); got != time.Second {
		t.Fatalf("RetryAfter() = %v, want %v", got, time.Second)
	}
}
