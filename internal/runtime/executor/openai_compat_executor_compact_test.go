package executor

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorNormalizesBigModelWebSearchTool(t *testing.T) {
	executor := NewOpenAICompatExecutor("bigmodel-coding", &config.Config{})
	payload := []byte(`{
		"model":"glm-5.1",
		"messages":[{"role":"user","content":"latest news"}],
		"tools":[{"type":"web_search_preview","search_context_size":"high"}],
		"tool_choice":{"type":"web_search_preview"}
	}`)

	out, err := executor.normalizeBigModelTools(payload, "https://open.bigmodel.cn/api/coding/paas/v4")
	if err != nil {
		t.Fatalf("normalizeBigModelTools error: %v", err)
	}
	if got := gjson.GetBytes(out, "tools.0.web_search.enable").Bool(); !got {
		t.Fatalf("tools.0.web_search.enable = false, want true: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.web_search.search_engine").String(); got != "search_pro" {
		t.Fatalf("tools.0.web_search.search_engine = %q, want search_pro: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.web_search.content_size").String(); got != "high" {
		t.Fatalf("tools.0.web_search.content_size = %q, want high: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.search_context_size").Exists() {
		t.Fatalf("unexpected OpenAI web_search field in BigModel tool: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("BigModel web_search should remove unsupported tool_choice object: %s", string(out))
	}
}

func TestOpenAICompatExecutorNormalizesBigModelMCPTool(t *testing.T) {
	executor := NewOpenAICompatExecutor("bigmodel-coding", &config.Config{})
	payload := []byte(`{
		"model":"glm-5.1",
		"messages":[{"role":"user","content":"use mcp"}],
		"tools":[{"type":"mcp","server_label":"web-search-prime","server_url":"https://open.bigmodel.cn/api/mcp/web_search_prime/mcp","headers":{"Authorization":"Bearer test"}}],
		"tool_choice":{"type":"mcp"}
	}`)

	out, err := executor.normalizeBigModelTools(payload, "https://open.bigmodel.cn/api/coding/paas/v4")
	if err != nil {
		t.Fatalf("normalizeBigModelTools error: %v", err)
	}
	if got := gjson.GetBytes(out, "tools.0.mcp.server_label").String(); got != "web-search-prime" {
		t.Fatalf("tools.0.mcp.server_label = %q, want web-search-prime: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.mcp.transport_type").String(); got != "streamable-http" {
		t.Fatalf("tools.0.mcp.transport_type = %q, want streamable-http: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.server_url").Exists() {
		t.Fatalf("unexpected flat MCP field in BigModel tool: %s", string(out))
	}
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("BigModel MCP should remove unsupported tool_choice object: %s", string(out))
	}
}

func TestBigModelCodingExecutorInjectsOfficialMCPTools(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","model":"glm-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewBigModelCodingExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/api/coding/paas/v4",
		"api_key":  "sk-test",
	}}
	payload := []byte(`{"model":"glm-5.1","messages":[{"role":"user","content":"read https://example.com and search current docs"}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.1",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(gotBody, `tools.#(mcp.server_label=="web-search-prime").mcp.server_url`).String(); got != "https://api.z.ai/api/mcp/web_search_prime/mcp" {
		t.Fatalf("web-search-prime MCP url = %q; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, `tools.#(mcp.server_label=="web-reader").mcp.server_url`).String(); got != "https://api.z.ai/api/mcp/web_reader/mcp" {
		t.Fatalf("web-reader MCP url = %q; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, `tools.#(mcp.server_label=="web-reader").mcp.transport_type`).String(); got != "streamable-http" {
		t.Fatalf("web-reader transport_type = %q; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, `tools.#(mcp.server_label=="web-search-prime").mcp.headers.Authorization`).String(); got != "Bearer sk-test" {
		t.Fatalf("web-search-prime Authorization = %q; body=%s", got, string(gotBody))
	}
}

func TestOpenAICompatExecutorDoesNotInjectBigModelCodingMCPToolsByDefault(t *testing.T) {
	payload := []byte(`{"model":"glm-5.1","messages":[{"role":"user","content":"hello"}]}`)

	out, err := NewBigModelCodingExecutor(&config.Config{}).injectOfficialMCPTools(payload, "glm-4.5", "sk-test")
	if err != nil {
		t.Fatalf("injectOfficialMCPTools error: %v", err)
	}
	if string(out) != string(payload) {
		t.Fatalf("non glm-5.1 model should not inject tools: %s", string(out))
	}
}

func TestBigModelCodingExecutorNormalizesThinkingAndToolParallelism(t *testing.T) {
	executor := NewBigModelCodingExecutor(&config.Config{})
	payload := []byte(`{
		"model":"glm-5.1",
		"stream":true,
		"messages":[{"role":"user","content":"hi"}],
		"reasoning":{"effort":"high"},
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`)

	out, err := executor.normalizeBigModelCodingPayload(payload, "glm-5.1")
	if err != nil {
		t.Fatalf("normalizeBigModelCodingPayload error: %v", err)
	}
	if got := gjson.GetBytes(out, "enable_thinking").Bool(); !got {
		t.Fatalf("enable_thinking = false, want true: %s", out)
	}
	if got := int(gjson.GetBytes(out, "thinking_budget").Int()); got != 24576 {
		t.Fatalf("thinking_budget = %d, want 24576: %s", got, out)
	}
	if got := gjson.GetBytes(out, "parallel_tool_calls").Bool(); !got {
		t.Fatalf("parallel_tool_calls = false, want true: %s", out)
	}
	if got := gjson.GetBytes(out, "tool_stream").Bool(); !got {
		t.Fatalf("tool_stream = false, want true for streaming tool calls: %s", out)
	}
	if gjson.GetBytes(out, "reasoning").Exists() || gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("OpenAI/Codex thinking fields should be removed: %s", out)
	}
}

func TestBigModelCodingExecutorDoesNotSetToolStreamForNonStreamingTools(t *testing.T) {
	executor := NewBigModelCodingExecutor(&config.Config{})
	payload := []byte(`{
		"model":"glm-5.1",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`)

	out, err := executor.normalizeBigModelCodingPayload(payload, "glm-5.1")
	if err != nil {
		t.Fatalf("normalizeBigModelCodingPayload error: %v", err)
	}
	if got := gjson.GetBytes(out, "parallel_tool_calls").Bool(); !got {
		t.Fatalf("parallel_tool_calls = false, want true: %s", out)
	}
	if gjson.GetBytes(out, "tool_stream").Exists() {
		t.Fatalf("tool_stream should only be set for streaming tool calls: %s", out)
	}
}

func TestBigModelCodingExecutorAddsRequestUserInputGuidanceForToolPayloads(t *testing.T) {
	executor := NewBigModelCodingExecutor(&config.Config{})
	payload := []byte(`{
		"model":"glm-5.1",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"request_user_input","parameters":{"type":"object"}}}]
	}`)

	out, err := executor.normalizeBigModelCodingPayload(payload, "glm-5.1")
	if err != nil {
		t.Fatalf("normalizeBigModelCodingPayload error: %v", err)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "system" {
		t.Fatalf("messages.0.role = %q, want system: %s", got, out)
	}
	guidance := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(guidance, "request_user_input tool") {
		t.Fatalf("missing request_user_input guidance: %s", out)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "user" {
		t.Fatalf("messages.1.role = %q, want user: %s", got, out)
	}
}

func TestBigModelCodingExecutorDoesNotAddRequestUserInputGuidanceWithoutTools(t *testing.T) {
	executor := NewBigModelCodingExecutor(&config.Config{})
	payload := []byte(`{"model":"glm-5.1","messages":[{"role":"user","content":"hi"}]}`)

	out, err := executor.normalizeBigModelCodingPayload(payload, "glm-5.1")
	if err != nil {
		t.Fatalf("normalizeBigModelCodingPayload error: %v", err)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "user" {
		t.Fatalf("messages.0.role = %q, want user: %s", got, out)
	}
	if strings.Contains(string(out), bigModelCodingUserInputGuidanceMarker) {
		t.Fatalf("guidance should not be added without tools: %s", out)
	}
}

func TestBigModelCodingExecutorTreatsPostDataRawJSONErrorAsTerminalDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_tool_tail_error","object":"chat.completion.chunk","created":1779410449,"model":"glm-5.1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"gh pr list\"}"}}]},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`{"error":{"code":"500","message":"内部错误"}}` + "\n"))
	}))
	defer server.Close()

	executor := NewBigModelCodingExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/api/coding/paas/v4",
		"api_key":  "sk-test",
	}}
	payload := []byte(`{"model":"glm-5.1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"list PRs"}]}],"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}}],"stream":true}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.1",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var joined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		joined.Write(chunk.Payload)
	}
	out := joined.String()
	if !strings.Contains(out, "response.function_call_arguments.done") {
		t.Fatalf("missing function_call_arguments.done: %s", out)
	}
	if !strings.Contains(out, "response.completed") {
		t.Fatalf("missing response.completed: %s", out)
	}
	if strings.Contains(out, "Upstream request failed") {
		t.Fatalf("unexpected upstream error surfaced: %s", out)
	}
}

func TestBigModelCodingExecutorDisablesThinkingFromCodexNone(t *testing.T) {
	executor := NewBigModelCodingExecutor(&config.Config{})
	payload := []byte(`{
		"model":"glm-5.1",
		"messages":[{"role":"user","content":"hi"}],
		"thinking":{"type":"disabled","budget_tokens":8192},
		"reasoning_effort":"high"
	}`)

	out, err := executor.normalizeBigModelCodingPayload(payload, "glm-5.1")
	if err != nil {
		t.Fatalf("normalizeBigModelCodingPayload error: %v", err)
	}
	if got := gjson.GetBytes(out, "enable_thinking").Bool(); got {
		t.Fatalf("enable_thinking = true, want false: %s", out)
	}
	if gjson.GetBytes(out, "thinking").Exists() || gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("source thinking fields should be removed: %s", out)
	}
	if gjson.GetBytes(out, "thinking_budget").Exists() {
		t.Fatalf("thinking_budget should not be set when thinking is disabled: %s", out)
	}
}

func TestBigModelCodingExecutorLeavesThinkingDefaultWhenUnset(t *testing.T) {
	executor := NewBigModelCodingExecutor(&config.Config{})
	payload := []byte(`{"model":"glm-5.1","messages":[{"role":"user","content":"hi"}]}`)

	out, err := executor.normalizeBigModelCodingPayload(payload, "glm-5.1")
	if err != nil {
		t.Fatalf("normalizeBigModelCodingPayload error: %v", err)
	}
	if gjson.GetBytes(out, "enable_thinking").Exists() {
		t.Fatalf("enable_thinking should not be forced when unset: %s", out)
	}
}

func TestRedactSensitiveJSONForLogMasksNestedMCPHeaders(t *testing.T) {
	body := []byte(`{"tools":[{"type":"mcp","mcp":{"headers":{"Authorization":"Bearer sk-secret-token"}}}],"api_key":"sk-other-secret"}`)

	out := redactSensitiveJSONForLog(body)
	if strings.Contains(string(out), "sk-secret-token") || strings.Contains(string(out), "sk-other-secret") {
		t.Fatalf("sensitive values were not redacted: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.mcp.headers.Authorization").String(); got != "Bearer sk-s...oken" {
		t.Fatalf("Authorization redaction = %q; body=%s", got, string(out))
	}
}

func TestOpenAICompatExecutorLeavesOtherProviderWebSearchToolUnchanged(t *testing.T) {
	executor := NewOpenAICompatExecutor("openrouter", &config.Config{})
	payload := []byte(`{"model":"gpt-5","tools":[{"type":"web_search","search_context_size":"high"}]}`)

	out, err := executor.normalizeBigModelTools(payload, "https://openrouter.ai/api/v1")
	if err != nil {
		t.Fatalf("normalizeBigModelTools error: %v", err)
	}
	if string(out) != string(payload) {
		t.Fatalf("payload changed for non-BigModel provider: %s", string(out))
	}
}

func TestOpenAICompatExecutorStreamAddsKimiReasoningForAssistantToolCalls(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("opencode-go", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{
		"model":"kimi-k2.6",
		"max_tokens":1024,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"Bash:3","name":"Bash","input":{"cmd":"pwd"}},
				{"type":"tool_use","id":"Read:2","name":"Read","input":{"file_path":"README.md"}}
			]}
		],
		"tools":[
			{"name":"Bash","description":"Run command","input_schema":{"type":"object"}},
			{"name":"Read","description":"Read file","input_schema":{"type":"object"}}
		]
	}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}

	reasoning := gjson.GetBytes(gotBody, "messages.1.reasoning_content")
	if !reasoning.Exists() {
		t.Fatalf("messages.1.reasoning_content should exist in upstream body: %s", string(gotBody))
	}
	if reasoning.String() == "" {
		t.Fatalf("messages.1.reasoning_content should not be empty")
	}
}
func TestOpenAICompatExecutorCompactPassthrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"gpt-5.1-codex-max","input":[{"role":"user","content":"hi"}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.1-codex-max",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses/compact" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses/compact")
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected input in body")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected messages in body")
	}
	if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorPayloadOverrideWinsOverThinkingSuffix(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "custom-openai", Protocol: "openai"},
					},
					Params: map[string]any{
						"reasoning_effort": "low",
					},
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"custom-openai(high)","messages":[{"role":"user","content":"hi"}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-openai(high)",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(gotBody, "reasoning_effort").String(); got != "low" {
		t.Fatalf("reasoning_effort = %q, want %q; body=%s", got, "low", string(gotBody))
	}
}

func TestOpenAICompatExecutorIdentityFingerprintOverridesProviderHeaders(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","model":"glm-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		IdentityFingerprint: config.IdentityFingerprintConfig{
			Codex: config.CodexIdentityFingerprintConfig{
				Enabled:     true,
				UserAgent:   "Codex Desktop/test",
				Version:     "0.130.0-alpha.5",
				Originator:  "Codex Desktop",
				SessionMode: "fixed",
				SessionID:   "server-session",
			},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":             server.URL + "/v1",
		"api_key":              "test",
		"identity_fingerprint": "codex",
		"header:User-Agent":    "codex-tui/old",
		"header:Version":       "0.124.0",
		"header:Originator":    "codex-tui",
		"header:X-Keep":        "ok",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gotHeaders.Get("User-Agent"); got != "Codex Desktop/test" {
		t.Fatalf("User-Agent = %q, want fingerprint value", got)
	}
	if got := gotHeaders.Get("Version"); got != "0.130.0-alpha.5" {
		t.Fatalf("Version = %q, want fingerprint value", got)
	}
	if got := gotHeaders.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %q, want fingerprint value", got)
	}
	if got := gotHeaders.Get("Session_id"); got != "server-session" {
		t.Fatalf("Session_id = %q, want fingerprint value", got)
	}
	if got := gotHeaders.Get("X-Keep"); got != "ok" {
		t.Fatalf("X-Keep = %q, want custom provider header preserved", got)
	}
}

func TestOpenAICompatExecutorStreamRejectsPlainJSONAfterBlankLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("\n\n: openrouter processing\n\nevent: error\n"))
		_, _ = w.Write([]byte(`{"error":{"message":"upstream failed","type":"server_error"}}` + "\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "openrouter-model",
		Payload: []byte(`{"model":"openrouter-model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var gotErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			break
		}
	}
	if gotErr == nil {
		t.Fatalf("expected plain JSON stream error")
	}
	if status, ok := gotErr.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("stream error status = %v, want %d", gotErr, http.StatusBadGateway)
	}
	if !strings.Contains(gotErr.Error(), "upstream failed") {
		t.Fatalf("stream error = %v", gotErr)
	}
}

func TestOpenAICompatExecutorStreamSkipsKeepAliveUntilDataLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("\n\n: openrouter processing\n\nevent: ping\nid: 1\nretry: 1000\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}` + "\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "openrouter-model",
		Payload: []byte(`{"model":"openrouter-model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var got strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		got.Write(chunk.Payload)
	}
	if gjson.Get(got.String(), "choices.0.delta.content").String() != "hello" {
		t.Fatalf("stream payload = %s", got.String())
	}
}

func TestOpenAICompatExecutorPassesImageGenerationsToImagesEndpoint(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1770000000,"data":[{"url":"https://example.com/image.png"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("qwen tokenplan", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"qwen-image-2.0","prompt":"draw a red square","size":"1024x1024"}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen-image-2.0",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Alt:          "images/generations",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/images/generations" {
		t.Fatalf("path = %q, want /v1/images/generations", gotPath)
	}
	if got := gjson.GetBytes(gotBody, "prompt").String(); got != "draw a red square" {
		t.Fatalf("prompt = %q", got)
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat messages in image generation body: %s", string(gotBody))
	}
	if string(resp.Payload) != `{"created":1770000000,"data":[{"url":"https://example.com/image.png"}]}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorPassesImageEditsToImagesEndpointByDefault(t *testing.T) {
	var gotPath string
	var gotForm map[string][]string
	var gotImage []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parse content type: %v", err)
		}
		if mediaType != "multipart/form-data" {
			t.Fatalf("content type = %q, want multipart/form-data", mediaType)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		form, err := reader.ReadForm(1 << 20)
		if err != nil {
			t.Fatalf("read multipart form: %v", err)
		}
		gotForm = form.Value
		file, err := form.File["image"][0].Open()
		if err != nil {
			t.Fatalf("open image part: %v", err)
		}
		gotImage, _ = io.ReadAll(file)
		_ = file.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1770000000,"data":[{"b64_json":"aW1hZ2U="}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("qwen tokenplan", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{
		"model":"qwen-image-2.0",
		"prompt":"extract the print",
		"size":"1024x1024",
		"image_files":[{"file_name":"shirt.png","content_type":"image/png","data_base64":"aGVsbG8="}]
	}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen-image-2.0",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Alt:          "images/edits",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/images/edits" {
		t.Fatalf("path = %q, want /v1/images/edits", gotPath)
	}
	if got := firstFormValue(gotForm, "prompt"); got != "extract the print" {
		t.Fatalf("prompt = %q", got)
	}
	if got := firstFormValue(gotForm, "model"); got != "qwen-image-2.0" {
		t.Fatalf("model = %q", got)
	}
	if string(gotImage) != "hello" {
		t.Fatalf("image data = %q", string(gotImage))
	}
	if string(resp.Payload) != `{"created":1770000000,"data":[{"b64_json":"aW1hZ2U="}]}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func firstFormValue(values map[string][]string, key string) string {
	if len(values[key]) == 0 {
		return ""
	}
	return values[key][0]
}
