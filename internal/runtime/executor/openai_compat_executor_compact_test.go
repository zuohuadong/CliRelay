package executor

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
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

func TestAstronCodeExecutorDowngradesRequiredToolChoiceToAuto(t *testing.T) {
	executor := NewAstronCodeExecutor(&config.Config{})
	payload := []byte(`{
		"model":"astron-code-latest",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"noop","parameters":{"type":"object"}}}],
		"tool_choice":"required"
	}`)

	out, err := executor.normalizeAstronPayload(payload, "astron-code-latest")
	if err != nil {
		t.Fatalf("normalizeAstronPayload error: %v", err)
	}
	if got := gjson.GetBytes(out, "tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto: %s", got, string(out))
	}
}

func TestAstronCodeExecutorNormalizesWebSearchPreviewTool(t *testing.T) {
	executor := NewAstronCodeExecutor(&config.Config{})
	payload := []byte(`{
		"model":"astron-code-latest",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search_preview","search_context_size":"high"}],
		"tool_choice":{"type":"web_search_preview"}
	}`)

	out, err := executor.normalizeAstronPayload(payload, "astron-code-latest")
	if err != nil {
		t.Fatalf("normalizeAstronPayload error: %v", err)
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.search_context_size").String(); got != "high" {
		t.Fatalf("tools.0.search_context_size = %q, want high: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto: %s", got, string(out))
	}
}

func TestAstronCodeExecutorDropsOrphanToolMessages(t *testing.T) {
	executor := NewAstronCodeExecutor(&config.Config{})
	payload := []byte(`{
		"model":"astron-code-latest",
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"tool","tool_call_id":"call_missing","content":"not found"},
			{"role":"user","content":"continue"}
		]
	}`)

	out, err := executor.normalizeAstronPayload(payload, "astron-code-latest")
	if err != nil {
		t.Fatalf("normalizeAstronPayload error: %v", err)
	}
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2: %s", len(messages), out)
	}
	for i, msg := range messages {
		if got := msg.Get("role").String(); got == "tool" {
			t.Fatalf("messages.%d should not be orphan tool message: %s", i, out)
		}
	}
}

func TestAstronCodeExecutorDropsUnansweredAssistantToolCalls(t *testing.T) {
	executor := NewAstronCodeExecutor(&config.Config{})
	payload := []byte(`{
		"model":"astron-code-latest",
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"","tool_calls":[{"id":"call_missing","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"user","content":"continue"}
		]
	}`)

	out, err := executor.normalizeAstronPayload(payload, "astron-code-latest")
	if err != nil {
		t.Fatalf("normalizeAstronPayload error: %v", err)
	}
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2: %s", len(messages), out)
	}
	for i, msg := range messages {
		if msg.Get("tool_calls").Exists() {
			t.Fatalf("messages.%d should not keep unanswered tool_calls: %s", i, out)
		}
	}
}

func TestAstronCodeExecutorPreservesCompleteToolExchange(t *testing.T) {
	executor := NewAstronCodeExecutor(&config.Config{})
	payload := []byte(`{
		"model":"astron-code-latest",
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"","tool_calls":[{"id":"call_ok","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_ok","content":"result"},
			{"role":"user","content":"continue"}
		]
	}`)

	out, err := executor.normalizeAstronPayload(payload, "astron-code-latest")
	if err != nil {
		t.Fatalf("normalizeAstronPayload error: %v", err)
	}
	if got := gjson.GetBytes(out, "messages.1.tool_calls.0.id").String(); got != "call_ok" {
		t.Fatalf("complete assistant tool call was not preserved: %s", out)
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "call_ok" {
		t.Fatalf("complete tool result was not preserved: %s", out)
	}
}

func TestAstronCodeExecutorDoesNotTreatPriorOrphanToolAsAnswer(t *testing.T) {
	executor := NewAstronCodeExecutor(&config.Config{})
	payload := []byte(`{
		"model":"astron-code-latest",
		"messages":[
			{"role":"tool","tool_call_id":"call_late","content":"prior orphan"},
			{"role":"assistant","content":"","tool_calls":[{"id":"call_late","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"user","content":"continue"}
		]
	}`)

	out, err := executor.normalizeAstronPayload(payload, "astron-code-latest")
	if err != nil {
		t.Fatalf("normalizeAstronPayload error: %v", err)
	}
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1: %s", len(messages), out)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("remaining message role = %q, want user: %s", got, out)
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

func TestOpenAICompatExecutorAppliesConfiguredMultimodalAdapterForTextModel(t *testing.T) {
	var extractorBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read extractor body: %v", err)
		}
		extractorBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"The image shows a generic text-model error screen."}`))
	}))
	defer server.Close()

	enabled := true
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		SDKConfig: config.SDKConfig{MultimodalAdapters: config.MultimodalAdaptersConfig{
			Enabled:       &enabled,
			DefaultAction: "extract",
			InjectAs:      "visual_context",
			Rules: []config.MultimodalAdapterRule{
				{
					Name:      "text-model-vision",
					Extractor: "vision",
					Match: config.MultimodalAdapterMatch{
						RequestedModels:   []string{"client-text-model"},
						UpstreamProviders: []string{"openai-compatibility"},
						UpstreamModels:    []string{"provider-text-model"},
						Protocols:         []string{"openai-response"},
					},
				},
			},
			Extractors: []config.MultimodalExtractorConfig{
				{Name: "vision", Type: "http", Endpoint: server.URL},
			},
		}},
	})
	payload := []byte(`{"model":"client-text-model","input":[{"role":"user","content":[{"type":"input_text","text":"what is shown?"},{"type":"input_image","image_url":"https://example.com/screenshot.png"}]}]}`)

	out, err := executor.applyMultimodalAdapter(context.Background(), payload, "provider-text-model", "openai-response", "client-text-model")
	if err != nil {
		t.Fatalf("applyMultimodalAdapter error: %v", err)
	}
	if extractorBody == "" || !strings.Contains(extractorBody, "screenshot.png") {
		t.Fatalf("extractor body = %q, want image ref", extractorBody)
	}
	body := string(out)
	if strings.Contains(body, "input_image") || strings.Contains(body, "image_url") {
		t.Fatalf("media was not stripped: %s", body)
	}
	if !strings.Contains(body, "visual_context") || !strings.Contains(body, "text-model error screen") {
		t.Fatalf("visual context was not injected: %s", body)
	}
}

func TestBigModelCodingExecutorAppliesConfiguredMultimodalAdapterForGLM52(t *testing.T) {
	var extractorBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read extractor body: %v", err)
		}
		extractorBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"The image shows a red error dialog."}`))
	}))
	defer server.Close()

	enabled := true
	executor := NewBigModelCodingExecutor(&config.Config{
		SDKConfig: config.SDKConfig{MultimodalAdapters: config.MultimodalAdaptersConfig{
			Enabled:       &enabled,
			DefaultAction: "extract",
			InjectAs:      "visual_context",
			Rules: []config.MultimodalAdapterRule{
				{
					Name:      "configured-codex-vision",
					Extractor: "vision",
					Match: config.MultimodalAdapterMatch{
						RequestedModels:   []string{"gpt-5.3-codex"},
						UpstreamProviders: []string{"bigmodel-coding"},
						UpstreamModels:    []string{"glm-5.2"},
						Protocols:         []string{"openai-response"},
					},
				},
			},
			Extractors: []config.MultimodalExtractorConfig{
				{Name: "vision", Type: "http", Endpoint: server.URL},
			},
		}},
	})
	payload := []byte(`{"model":"gpt-5.3-codex","input":[{"role":"user","content":[{"type":"input_text","text":"what is shown?"},{"type":"input_image","image_url":"https://example.com/screenshot.png"}]}]}`)

	out, err := executor.applyMultimodalAdapter(context.Background(), payload, "glm-5.2", "openai-response", "gpt-5.3-codex")
	if err != nil {
		t.Fatalf("applyMultimodalAdapter error: %v", err)
	}
	if extractorBody == "" || !strings.Contains(extractorBody, "screenshot.png") {
		t.Fatalf("extractor body = %q, want image ref", extractorBody)
	}
	body := string(out)
	if strings.Contains(body, "input_image") || strings.Contains(body, "image_url") {
		t.Fatalf("media was not stripped: %s", body)
	}
	if !strings.Contains(body, "visual_context") || !strings.Contains(body, "red error dialog") {
		t.Fatalf("visual context was not injected: %s", body)
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

func TestAstronCodeExecutorDoesNotRepeatStreamingToolCallIDOnArgumentDeltas(t *testing.T) {
	seq := &astronToolCallIDSeq{}
	first := ensureAstronToolCallIDs([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"read","arguments":""}}]}}]}`), seq)
	second := ensureAstronToolCallIDs([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"file\":\"README.md\"}"}}]}}]}`), seq)

	firstID := gjson.Get(strings.TrimSpace(strings.TrimPrefix(string(first), "data:")), "choices.0.delta.tool_calls.0.id").String()
	secondID := gjson.Get(strings.TrimSpace(strings.TrimPrefix(string(second), "data:")), "choices.0.delta.tool_calls.0.id").String()
	if firstID == "" {
		t.Fatalf("first synthetic id is empty: %s", first)
	}
	if secondID != "" {
		t.Fatalf("argument delta should not repeat synthetic id, got %q: %s", secondID, second)
	}
}

func TestAstronCodeExecutorGeneratesDistinctNonStreamToolCallIDs(t *testing.T) {
	body := ensureAstronNonStreamToolCallIDs([]byte(`{"choices":[{"index":0,"message":{"tool_calls":[{"type":"function","function":{"name":"read","arguments":"{}"}},{"type":"function","function":{"name":"glob","arguments":"{}"}}]}}]}`))

	firstID := gjson.GetBytes(body, "choices.0.message.tool_calls.0.id").String()
	secondID := gjson.GetBytes(body, "choices.0.message.tool_calls.1.id").String()
	if firstID == "" || secondID == "" {
		t.Fatalf("synthetic ids should be present: %s", body)
	}
	if firstID == secondID {
		t.Fatalf("synthetic ids should be distinct, got %q", firstID)
	}
}

func TestAstronCodeExecutorStreamRejectsDoneOnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewAstronCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "sk-test",
	}}
	payload := []byte(`{"model":"xopglm52","input":"hi","stream":true}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "xopglm52",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: payload,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var got strings.Builder
	var gotErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			break
		}
		got.Write(chunk.Payload)
	}
	if got.String() != "" {
		t.Fatalf("expected no payload before empty-stream error, got %q", got.String())
	}
	if gotErr == nil {
		t.Fatal("expected done-only stream error")
	}
	if status, ok := gotErr.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("stream error status = %v, want %d", gotErr, http.StatusBadGateway)
	}
	if !strings.Contains(gotErr.Error(), "empty stream response") {
		t.Fatalf("stream error = %v", gotErr)
	}
}

func TestAstronCodeExecutorResponseEndpointFallsBackToChatOnDoneOnlyStream(t *testing.T) {
	var paths []string
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/v1/responses":
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		case "/v2/chat/completions":
			_, _ = w.Write([]byte(`data: {"id":"chatcmpl-fallback","object":"chat.completion.chunk","model":"xopglm52","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":null}]}` + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	executor := NewAstronCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          server.URL + "/v1",
		"api_key":           "sk-test",
		"response_endpoint": "true",
	}}
	payload := []byte(`{"model":"deepseek-v4-pro","input":"hi","stream":true}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "xopglm52",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: payload,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var got strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v; stream=%s", chunk.Err, got.String())
		}
		got.Write(chunk.Payload)
	}
	if !strings.Contains(got.String(), "OK") {
		t.Fatalf("fallback stream missing chat payload: %s", got.String())
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want responses primary then chat fallback", paths)
	}
	if paths[0] != "/v1/responses" || paths[1] != "/v2/chat/completions" {
		t.Fatalf("paths = %v, want [/v1/responses /v2/chat/completions]", paths)
	}
	if !gjson.GetBytes(bodies[0], "input").Exists() {
		t.Fatalf("responses request should keep input payload, got %s", string(bodies[0]))
	}
	if !gjson.GetBytes(bodies[1], "messages").Exists() {
		t.Fatalf("chat fallback request should use messages payload, got %s", string(bodies[1]))
	}
}

func TestAstronCodeExecutorResponseEndpointFallsBackToChatOnSchemaRouteNotFound(t *testing.T) {
	var paths []string
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, body)
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"no any schema route found","sid":"ase-test"}`))
		case "/v2/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"id":"chatcmpl-fallback","object":"chat.completion.chunk","model":"xopdeepseekv4pro","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":null}]}` + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	executor := NewAstronCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          server.URL + "/v1",
		"api_key":           "sk-test",
		"response_endpoint": "true",
	}}
	payload := []byte(`{"model":"deepseek-v4-pro","input":"hi","stream":true}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "xopdeepseekv4pro",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: payload,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var got strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v; stream=%s", chunk.Err, got.String())
		}
		got.Write(chunk.Payload)
	}
	if !strings.Contains(got.String(), "OK") {
		t.Fatalf("fallback stream missing chat payload: %s", got.String())
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want responses primary then chat fallback", paths)
	}
	if paths[0] != "/v1/responses" || paths[1] != "/v2/chat/completions" {
		t.Fatalf("paths = %v, want [/v1/responses /v2/chat/completions]", paths)
	}
	if !gjson.GetBytes(bodies[0], "input").Exists() {
		t.Fatalf("responses request should keep input payload, got %s", string(bodies[0]))
	}
	if !gjson.GetBytes(bodies[1], "messages").Exists() {
		t.Fatalf("chat fallback request should use messages payload, got %s", string(bodies[1]))
	}
}

func TestAstronCodeExecutorDropsStreamingToolCallsWithEmptyName(t *testing.T) {
	seq := &astronToolCallIDSeq{}
	input := []byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"read","arguments":"{}"}},{"index":1,"type":"function","function":{"name":"","arguments":"{}"}},{"index":2,"type":"function","function":{"name":"glob","arguments":"{}"}}]}}]}`)
	out := ensureAstronToolCallIDs(input, seq)
	tcs := gjson.Get(strings.TrimSpace(strings.TrimPrefix(string(out), "data:")), "choices.0.delta.tool_calls").Array()
	if len(tcs) != 2 {
		t.Fatalf("expected 2 tool_calls after dropping empty-name one, got %d: %s", len(tcs), out)
	}
	if got := tcs[0].Get("function.name").String(); got != "read" {
		t.Fatalf("first tool_call name = %q, want read", got)
	}
	if got := tcs[1].Get("function.name").String(); got != "glob" {
		t.Fatalf("second tool_call name = %q, want glob", got)
	}
}

func TestAstronCodeExecutorDropsNonStreamToolCallsWithEmptyName(t *testing.T) {
	input := []byte(`{"choices":[{"index":0,"message":{"tool_calls":[{"type":"function","function":{"name":"read","arguments":"{}"}},{"type":"function","function":{"name":"","arguments":"{}"}}]}}]}`)
	body := ensureAstronNonStreamToolCallIDs(input)
	tcs := gjson.GetBytes(body, "choices.0.message.tool_calls").Array()
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call after dropping empty-name one, got %d: %s", len(tcs), body)
	}
	if got := tcs[0].Get("function.name").String(); got != "read" {
		t.Fatalf("remaining tool_call name = %q, want read", got)
	}
	if id := tcs[0].Get("id").String(); id == "" {
		t.Fatalf("remaining tool_call should have synthetic id: %s", body)
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

func TestBigModelCodingExecutorStreamsResponsesCompactionTriggerAsSingleCompactionItem(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","model":"glm-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"stream summary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`))
	}))
	defer server.Close()

	executor := NewBigModelCodingExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"glm-5.2","stream":true,"input":[{"type":"message","role":"user","content":"hello"},{"type":"compaction_trigger"}]}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
		Stream:          true,
		Headers:         http.Header{"X-Codex-Turn-Metadata": []string{`{"request_kind":"compaction"}`}},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamed bytes.Buffer
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil && streamErr == nil {
			streamErr = chunk.Err
			continue
		}
		streamed.Write(chunk.Payload)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions; stream_error=%v", gotPath, streamErr)
	}
	if streamErr != nil {
		t.Fatalf("stream chunk error = %v", streamErr)
	}
	if got := gjson.GetBytes(gotBody, "messages.0.content").String(); got != responsesCompactSystemPrompt {
		t.Fatalf("system prompt = %q, want compact prompt; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("compact upstream request must be non-streaming: %s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "tools").Exists() {
		t.Fatalf("compact upstream request must not include tools: %s", string(gotBody))
	}

	completed := lastSSEDataPayloadForType(t, streamed.String(), "response.completed")
	if got := gjson.GetBytes(completed, "response.output.#").Int(); got != 1 {
		t.Fatalf("response.output length = %d, want 1; stream=%s", got, streamed.String())
	}
	if got := gjson.GetBytes(completed, "response.output.0.type").String(); got != "compaction" {
		t.Fatalf("response.output.0.type = %q, want compaction; stream=%s", got, streamed.String())
	}
	if got := gjson.GetBytes(completed, "response.output.0.encrypted_content").String(); got != "stream summary" {
		t.Fatalf("encrypted_content = %q, want stream summary; stream=%s", got, streamed.String())
	}
}

func TestOpenAICompatExecutorStreamsResponsesCompactionTriggerAsSingleCompactionItem(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","model":"custom-coder","output":[{"type":"compaction","encrypted_content":"stream summary"}],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"custom-coder","stream":true,"input":[{"type":"message","role":"user","content":"hello"},{"type":"compaction_trigger"}]}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-coder",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamed bytes.Buffer
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil && streamErr == nil {
			streamErr = chunk.Err
			continue
		}
		streamed.Write(chunk.Payload)
	}
	if gotPath != "/v1/responses/compact" {
		t.Fatalf("path = %q, want /v1/responses/compact; stream_error=%v", gotPath, streamErr)
	}
	if streamErr != nil {
		t.Fatalf("stream chunk error = %v", streamErr)
	}

	completed := lastSSEDataPayloadForType(t, streamed.String(), "response.completed")
	if got := gjson.GetBytes(completed, "response.output.#").Int(); got != 1 {
		t.Fatalf("response.output length = %d, want 1; stream=%s", got, streamed.String())
	}
	if got := gjson.GetBytes(completed, "response.output.0.type").String(); got != "compaction" {
		t.Fatalf("response.output.0.type = %q, want compaction; stream=%s", got, streamed.String())
	}
}

func TestOpenAICompatExecutorResponseEndpointPreservesResponsesImageGeneration(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"custom-gpt-5-5","output":[{"id":"ig_1","type":"image_generation_call","output_format":"png","result":"AA=="}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatible-custom-coding", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:             "custom-coding",
			BaseURL:          server.URL + "/v1",
			ResponseEndpoint: true,
			Models:           []config.OpenAICompatibilityModel{{Name: "custom-gpt-5-5", Alias: "gpt-5.5"}},
		}},
	})
	auth := &cliproxyauth.Auth{Provider: "openai-compatible-custom-coding", Attributes: map[string]string{
		"base_url":     server.URL + "/v1",
		"api_key":      "test",
		"compat_name":  "custom-coding",
		"provider_key": "openai-compatible-custom-coding",
	}}
	payload := []byte(`{"model":"custom-gpt-5-5","input":"draw","tools":[{"type":"image_generation"}],"stream":false}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-gpt-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected responses input body, got %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat messages body: %s", string(gotBody))
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.type").String(); got != "image_generation_call" {
		t.Fatalf("response output type = %q, want image_generation_call; body=%s", got, string(resp.Payload))
	}
}

func TestOpenAICompatExecutorResponseEndpointTranslatesChatCompletionsNonStream(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":123,"status":"completed","model":"custom-deepseek","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatible-custom-coding", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:             "custom-coding",
			BaseURL:          server.URL + "/v1",
			ResponseEndpoint: true,
			Models:           []config.OpenAICompatibilityModel{{Name: "custom-deepseek", Alias: "deepseek-v4-pro"}},
		}},
	})
	auth := &cliproxyauth.Auth{Provider: "openai-compatible-custom-coding", Attributes: map[string]string{
		"base_url":     server.URL + "/v1",
		"api_key":      "test",
		"compat_name":  "custom-coding",
		"provider_key": "openai-compatible-custom-coding",
	}}
	payload := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"max_tokens":16,"stream":false}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-deepseek",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected responses input body, got %s", gotBody)
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat messages body: %s", gotBody)
	}
	if got := gjson.GetBytes(gotBody, "max_output_tokens").Int(); got != 16 {
		t.Fatalf("max_output_tokens = %d, want 16; body=%s", got, gotBody)
	}
	if got := gjson.GetBytes(resp.Payload, "object").String(); got != "chat.completion" {
		t.Fatalf("response object = %q, want chat.completion; body=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "ok" {
		t.Fatalf("response content = %q, want ok; body=%s", got, resp.Payload)
	}
}

func TestOpenAICompatExecutorResponseEndpointTranslatesChatCompletionsStream(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":123,\"model\":\"custom-deepseek\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":123,\"status\":\"completed\",\"model\":\"custom-deepseek\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatible-custom-coding", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:             "custom-coding",
			BaseURL:          server.URL + "/v1",
			ResponseEndpoint: true,
			Models:           []config.OpenAICompatibilityModel{{Name: "custom-deepseek", Alias: "deepseek-v4-pro"}},
		}},
	})
	auth := &cliproxyauth.Auth{Provider: "openai-compatible-custom-coding", Attributes: map[string]string{
		"base_url":     server.URL + "/v1",
		"api_key":      "test",
		"compat_name":  "custom-coding",
		"provider_key": "openai-compatible-custom-coding",
	}}
	payload := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-deepseek",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	sawContent := false
	sawFinish := false
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if got := gjson.GetBytes(chunk.Payload, "choices.0.delta.content").String(); got == "ok" {
			sawContent = true
		}
		if got := gjson.GetBytes(chunk.Payload, "choices.0.finish_reason").String(); got == "stop" {
			sawFinish = true
		}
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if !gjson.GetBytes(gotBody, "input").Exists() || gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("expected translated responses body, got %s", gotBody)
	}
	if !sawContent || !sawFinish {
		t.Fatalf("missing translated chat stream content/finish: content=%v finish=%v", sawContent, sawFinish)
	}
}

func TestOpenAICompatExecutorResponseEndpointStreamsResponsesImageGeneration(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.image_generation_call.partial_image\",\"item_id\":\"ig_1\",\"partial_image_b64\":\"AA==\",\"partial_image_index\":0}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatible-custom-coding", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:             "custom-coding",
			BaseURL:          server.URL + "/v1",
			ResponseEndpoint: true,
			Models:           []config.OpenAICompatibilityModel{{Name: "custom-gpt-5-5", Alias: "gpt-5.5"}},
		}},
	})
	auth := &cliproxyauth.Auth{Provider: "openai-compatible-custom-coding", Attributes: map[string]string{
		"base_url":     server.URL + "/v1",
		"api_key":      "test",
		"compat_name":  "custom-coding",
		"provider_key": "openai-compatible-custom-coding",
	}}
	payload := []byte(`{"model":"custom-gpt-5-5","input":"draw","tools":[{"type":"image_generation"}],"stream":true}`)
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-gpt-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: payload,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected responses input body, got %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "messages").Exists() || gjson.GetBytes(gotBody, "stream_options").Exists() {
		t.Fatalf("unexpected chat-completions fields in body: %s", string(gotBody))
	}
}

func TestOpenAICompatExecutorCodexIdentityPreservesResponsesImageGeneration(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"custom-gpt-5-5","output":[{"id":"ig_1","type":"image_generation_call","output_format":"png","result":"AA=="}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatible-custom-coding", &config.Config{
		IdentityFingerprint: config.IdentityFingerprintConfig{
			Codex: config.CodexIdentityFingerprintConfig{Enabled: true},
		},
	})
	auth := &cliproxyauth.Auth{Provider: "openai-compatible-custom-coding", Attributes: map[string]string{
		"base_url":             server.URL + "/v1",
		"api_key":              "test",
		"identity_fingerprint": "codex",
	}}
	payload := []byte(`{"model":"custom-gpt-5-5","input":"draw","tools":[{"type":"image_generation"}],"stream":false}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-gpt-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if got := gjson.GetBytes(gotBody, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat messages body: %s", string(gotBody))
	}
}

func TestAstronCodeExecutorCompactUsesChatAndWrapsResponse(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","model":"astron-code-latest","choices":[{"index":0,"message":{"role":"assistant","content":"summary text"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`))
	}))
	defer server.Close()

	executor := NewAstronCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v2",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"gpt-5.3-codex","input":[{"role":"user","content":"hi"}],"tools":[{"type":"mcp","server_label":"web-reader"}],"tool_choice":"required"}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "astron-code-latest",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		Alt:             "responses/compact",
		OriginalRequest: payload,
		Stream:          false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v2/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v2/chat/completions")
	}
	if gjson.GetBytes(gotBody, "tools").Exists() {
		t.Fatalf("unexpected tools in compact chat body: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "messages.1.content").String(); !strings.Contains(got, `"content":"hi"`) {
		t.Fatalf("compact prompt did not include original transcript: %s", got)
	}
	if got := gjson.GetBytes(resp.Payload, "object").String(); got != "response.compaction" {
		t.Fatalf("object = %q, want response.compaction; body=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "output.#").Int(); got != 1 {
		t.Fatalf("output length = %d, want only one compaction item; body=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.type").String(); got != "compaction" {
		t.Fatalf("output.0.type = %q, want compaction; body=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.encrypted_content").String(); got != "summary text" {
		t.Fatalf("compaction content = %q, want summary text", got)
	}
	if gjson.GetBytes(resp.Payload, "output.0.created_by").Exists() {
		t.Fatalf("compact output should not include non-Codex created_by metadata: %s", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 11 {
		t.Fatalf("usage.input_tokens = %d, want 11", got)
	}
}

func TestBuildResponsesCompactResponseUsesSingleStrictCompactionItem(t *testing.T) {
	chatResponse := []byte(`{"choices":[{"message":{"content":"summary text"}}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`)
	resp, err := buildResponsesCompactResponse(
		"astron-code-latest",
		chatResponse,
		http.Header{"X-Test": []string{"ok"}},
	)
	if err != nil {
		t.Fatalf("buildResponsesCompactResponse error: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "output.#").Int(); got != 1 {
		t.Fatalf("output length = %d, want one compaction item; body=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.type").String(); got != "compaction" {
		t.Fatalf("output.0.type = %q, want compaction; body=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.encrypted_content").String(); got != "summary text" {
		t.Fatalf("output.0.encrypted_content = %q, want summary text", got)
	}
	for _, path := range []string{"output.0.role", "output.0.content", "output.0.created_by", "output.1"} {
		if gjson.GetBytes(resp.Payload, path).Exists() {
			t.Fatalf("%s should be absent from strict compaction output: %s", path, string(resp.Payload))
		}
	}
	if got := resp.Headers.Get("X-Test"); got != "ok" {
		t.Fatalf("headers were not preserved, got %q", got)
	}
}

func TestAstronCodeExecutorStreamsResponsesCompactionTriggerAsSingleCompactionItem(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","model":"astron-code-latest","choices":[{"index":0,"message":{"role":"assistant","content":"stream summary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`))
	}))
	defer server.Close()

	executor := NewAstronCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v2",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"deepseek-v4-pro","stream":true,"input":[{"type":"message","role":"user","content":"hello"},{"type":"compaction_trigger"}]}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "astron-code-latest",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
		Stream:          true,
		Headers:         http.Header{"X-Codex-Turn-Metadata": []string{`{"request_kind":"compaction"}`}},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	if gotPath != "/v2/chat/completions" {
		t.Fatalf("path = %q, want /v2/chat/completions", gotPath)
	}
	if got := gjson.GetBytes(gotBody, "messages.0.content").String(); got != responsesCompactSystemPrompt {
		t.Fatalf("system prompt = %q, want astron compact prompt; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("compact upstream chat request must be non-streaming: %s", string(gotBody))
	}

	var streamed bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	completed := lastSSEDataPayloadForType(t, streamed.String(), "response.completed")
	if got := gjson.GetBytes(completed, "response.output.#").Int(); got != 1 {
		t.Fatalf("response.output length = %d, want 1; stream=%s", got, streamed.String())
	}
	if got := gjson.GetBytes(completed, "response.output.0.type").String(); got != "compaction" {
		t.Fatalf("response.output.0.type = %q, want compaction; stream=%s", got, streamed.String())
	}
	if got := gjson.GetBytes(completed, "response.output.0.encrypted_content").String(); got != "stream summary" {
		t.Fatalf("encrypted_content = %q, want stream summary; stream=%s", got, streamed.String())
	}
}

func TestAstronCodeExecutorResponseEndpointUsesResponsesPath(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"astron-code-latest","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewAstronCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          server.URL + "/v2",
		"api_key":           "test",
		"response_endpoint": "true",
	}}
	payload := []byte(`{"model":"deepseek-v4-pro","input":[{"role":"user","content":"hi"}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "astron-code-latest",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected responses input body, got %s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat messages body: %s", string(gotBody))
	}
	if got := gjson.GetBytes(resp.Payload, "model").String(); got != "astron-code-latest" {
		t.Fatalf("response model = %q, want astron-code-latest; body=%s", got, string(resp.Payload))
	}
}

func TestAstronCodeExecutorResponseEndpointTranslatesChatCompletions(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":123,"status":"completed","model":"astron-code-latest","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewAstronCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          server.URL + "/v2",
		"api_key":           "test",
		"response_endpoint": "true",
	}}
	payload := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "astron-code-latest",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if !gjson.GetBytes(gotBody, "input").Exists() || gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("expected translated responses body, got %s", gotBody)
	}
	if got := gjson.GetBytes(resp.Payload, "object").String(); got != "chat.completion" {
		t.Fatalf("response object = %q, want chat.completion; body=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "ok" {
		t.Fatalf("response content = %q, want ok; body=%s", got, resp.Payload)
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
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","model":"glm-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		IdentityFingerprint: config.IdentityFingerprintConfig{
			Codex: config.CodexIdentityFingerprintConfig{
				Enabled:     true,
				UserAgent:   "codex-tui/test",
				Version:     "0.137.0",
				Originator:  "codex-tui",
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
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions when only identity fingerprint is configured", gotPath)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "ok" {
		t.Fatalf("response content = %q, want ok; body=%s", got, resp.Payload)
	}

	if got := gotHeaders.Get("User-Agent"); got != "codex-tui/test" {
		t.Fatalf("User-Agent = %q, want fingerprint value", got)
	}
	if got := gotHeaders.Get("Version"); got != "0.137.0" {
		t.Fatalf("Version = %q, want fingerprint value", got)
	}
	if got := gotHeaders.Get("Originator"); got != "codex-tui" {
		t.Fatalf("Originator = %q, want fingerprint value", got)
	}
	if got := gotHeaders.Get("Session_id"); got != "server-session" {
		t.Fatalf("Session_id = %q, want fingerprint value", got)
	}
	if got := gotHeaders.Get("X-Keep"); got != "ok" {
		t.Fatalf("X-Keep = %q, want custom provider header preserved", got)
	}
}

func TestOpenAICompatExecutorImagesGenerationsPassthrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"AA=="}],"usage":{"total_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-image",
		Payload: []byte(`{"model":"compat-image","prompt":"draw"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       false,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/images/generations" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/images/generations")
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", gotContentType)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "upstream-image" {
		t.Fatalf("model = %q, want upstream-image; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(resp.Payload, "data.0.b64_json").String(); got != "AA==" {
		t.Fatalf("response payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorVideosPassthrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"video_123","object":"video","status":"queued","progress":0}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "agnes-video-v2.0",
		Payload: []byte(`{"model":"agnes-video-v2.0","prompt":"animate"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-video"),
		Stream:       false,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/openai/v1/videos",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/videos" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/videos")
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", gotContentType)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "agnes-video-v2.0" {
		t.Fatalf("model = %q, want agnes-video-v2.0; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(resp.Payload, "id").String(); got != "video_123" {
		t.Fatalf("response payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorVideosRetrieveUsesGet(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody []byte
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task_123","object":"video","status":"completed","progress":100,"video_url":"https://example.com/video.mp4"}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "agnes-video-v2.0",
		Payload: []byte(`{"request_id":"task_123"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-video"),
		Stream:       false,
		Headers:      http.Header{"Content-Type": []string{"application/json"}},
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/openai/v1/videos/task_123",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/v1/videos/task_123" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/videos/task_123")
	}
	if len(gotBody) != 0 {
		t.Fatalf("body = %q, want empty", string(gotBody))
	}
	if gotContentType != "" {
		t.Fatalf("content type = %q, want empty", gotContentType)
	}
	if got := gjson.GetBytes(resp.Payload, "video_url").String(); got != "https://example.com/video.mp4" {
		t.Fatalf("response payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorVideosRetrieveReplacesRouteTemplateWithUpstreamID(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"upstream_123","object":"video","status":"completed","progress":100,"video_url":"https://example.com/video.mp4"}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "generic-video-model",
		Payload: []byte(`{"request_id":"upstream_123","video_id":"upstream_123"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-video"),
		Stream:       false,
		Headers:      http.Header{"Content-Type": []string{"application/json"}},
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/videos/:video_id",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/videos/upstream_123" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/videos/upstream_123")
	}
}

func TestOpenAICompatExecutorVideosContentRetrievesVideoMetadata(t *testing.T) {
	var gotMethod string
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task_123","object":"video","status":"completed","progress":100,"video_url":"https://example.com/video.mp4"}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "agnes-video-v2.0",
		Payload: []byte(`{"request_id":"task_123"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-video"),
		Stream:       false,
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/openai/v1/videos/task_123/content",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/v1/videos/task_123" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/videos/task_123")
	}
}

func TestOpenAICompatExecutorAgnesVideosRetrieveUsesAgnesAPI(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task_123","video_id":"video_abc","object":"video","status":"completed","progress":100,"video_url":"https://example.com/video.mp4"}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Label: "agnes",
		Attributes: map[string]string{
			"base_url":    server.URL + "/v1",
			"api_key":     "test",
			"compat_name": "agnes",
		},
	}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "agnes-video-v2.0",
		Payload: []byte(`{"request_id":"task_123","video_id":"video_abc"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-video"),
		Stream:       false,
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/openai/v1/videos/task_123",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/v1/agnesapi" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/agnesapi")
	}
	if gotQuery != "video_id=video_abc" {
		t.Fatalf("query = %q, want video_id=video_abc", gotQuery)
	}
}

func TestOpenAICompatExecutorImagesGenerationsStreamsUpstream(t *testing.T) {
	var gotPath string
	var gotBody []byte
	var gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: image_generation.partial\ndata: {\"type\":\"image_generation.partial\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-image",
		Payload: []byte(`{"model":"compat-image","prompt":"draw","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       true,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var streamed bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	if gotPath != "/v1/images/generations" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/images/generations")
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("accept = %q, want text/event-stream", gotAccept)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "upstream-image" {
		t.Fatalf("model = %q, want upstream-image; body=%s", got, string(gotBody))
	}
	if !gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("stream flag missing from upstream body: %s", string(gotBody))
	}
	if !strings.Contains(streamed.String(), "event: image_generation.partial") || !strings.Contains(streamed.String(), "data: [DONE]") {
		t.Fatalf("streamed body = %q", streamed.String())
	}
}

func TestOpenAICompatExecutorImagesEditsMultipartRewritesModel(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if errWrite := writer.WriteField("model", "compat-image"); errWrite != nil {
		t.Fatalf("write model field: %v", errWrite)
	}
	if errWrite := writer.WriteField("prompt", "edit"); errWrite != nil {
		t.Fatalf("write prompt field: %v", errWrite)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.png"))
	header.Set("Content-Type", "image/png")
	part, errCreate := writer.CreatePart(header)
	if errCreate != nil {
		t.Fatalf("create image field: %v", errCreate)
	}
	if _, errWrite := part.Write([]byte("png-data")); errWrite != nil {
		t.Fatalf("write image field: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}
	contentType := writer.FormDataContentType()

	var gotPath string
	var gotModel string
	var gotPrompt string
	var gotFile string
	var gotFileContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if errParse := r.ParseMultipartForm(32 << 20); errParse != nil {
			t.Fatalf("parse multipart form: %v", errParse)
		}
		gotModel = r.FormValue("model")
		gotPrompt = r.FormValue("prompt")
		file, fileHeader, errFile := r.FormFile("image")
		if errFile != nil {
			t.Fatalf("read image file: %v", errFile)
		}
		gotFileContentType = fileHeader.Header.Get("Content-Type")
		data, errRead := io.ReadAll(file)
		if errClose := file.Close(); errClose != nil {
			t.Fatalf("close image file: %v", errClose)
		}
		if errRead != nil {
			t.Fatalf("read image file: %v", errRead)
		}
		gotFile = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"AA=="}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-image",
		Payload: body.Bytes(),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       false,
		Headers: http.Header{
			"Content-Type": []string{contentType},
		},
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/edits",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/images/edits" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/images/edits")
	}
	if gotModel != "upstream-image" {
		t.Fatalf("model = %q, want upstream-image", gotModel)
	}
	if gotPrompt != "edit" {
		t.Fatalf("prompt = %q, want edit", gotPrompt)
	}
	if gotFile != "png-data" {
		t.Fatalf("file = %q, want png-data", gotFile)
	}
	if gotFileContentType != "image/png" {
		t.Fatalf("file content type = %q, want image/png", gotFileContentType)
	}
}

func TestRewriteOpenAICompatImagesMultipartPayloadPreservesStreamAndFileContentType(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if errWrite := writer.WriteField("model", "compat-image"); errWrite != nil {
		t.Fatalf("write model field: %v", errWrite)
	}
	if errWrite := writer.WriteField("stream", "false"); errWrite != nil {
		t.Fatalf("write stream field: %v", errWrite)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.webp"))
	header.Set("Content-Type", "image/webp")
	part, errCreate := writer.CreatePart(header)
	if errCreate != nil {
		t.Fatalf("create image field: %v", errCreate)
	}
	if _, errWrite := part.Write([]byte("webp-data")); errWrite != nil {
		t.Fatalf("write image field: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}

	out, contentType, err := prepareOpenAICompatImagesPayload(body.Bytes(), "upstream-image", writer.FormDataContentType(), true)
	if err != nil {
		t.Fatalf("prepareOpenAICompatImagesPayload error: %v", err)
	}
	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil {
		t.Fatalf("parse content type: %v", errParse)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("media type = %q, want multipart/form-data", mediaType)
	}
	reader := multipart.NewReader(bytes.NewReader(out), params["boundary"])
	form, errRead := reader.ReadForm(32 << 20)
	if errRead != nil {
		t.Fatalf("read rewritten form: %v", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			t.Fatalf("remove form files: %v", errRemove)
		}
	}()
	if got := form.Value["model"]; len(got) != 1 || got[0] != "upstream-image" {
		t.Fatalf("model values = %#v, want upstream-image", got)
	}
	if got := form.Value["stream"]; len(got) != 1 || got[0] != "true" {
		t.Fatalf("stream values = %#v, want true", got)
	}
	if got := form.File["image"]; len(got) != 1 || got[0].Header.Get("Content-Type") != "image/webp" {
		t.Fatalf("image headers = %#v, want image/webp", got)
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

func TestOpenAICompatExecutorStreamRejectsDoneOnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "openrouter-model",
		Payload: []byte(`{"model":"openrouter-model","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: []byte(`{"model":"openrouter-model","input":"hi","stream":true}`),
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var got strings.Builder
	var gotErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			break
		}
		got.Write(chunk.Payload)
	}
	if got.String() != "" {
		t.Fatalf("expected no payload before empty-stream error, got %q", got.String())
	}
	if gotErr == nil {
		t.Fatal("expected done-only stream error")
	}
	if status, ok := gotErr.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("stream error status = %v, want %d", gotErr, http.StatusBadGateway)
	}
	if !strings.Contains(gotErr.Error(), "empty stream response") {
		t.Fatalf("stream error = %v", gotErr)
	}
}

func TestOpenAICompatExecutorStreamRejectsUsageOnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_usage_only","object":"chat.completion.chunk","created":1779410449,"model":"openrouter-model","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "openrouter-model",
		Payload: []byte(`{"model":"openrouter-model","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: []byte(`{"model":"openrouter-model","input":"hi","stream":true}`),
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var got strings.Builder
	var gotErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			break
		}
		got.Write(chunk.Payload)
	}
	if got.String() != "" {
		t.Fatalf("expected lifecycle-only payload to be withheld before error, got %q", got.String())
	}
	if gotErr == nil {
		t.Fatal("expected usage-only stream error")
	}
	if status, ok := gotErr.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("stream error status = %v, want %d", gotErr, http.StatusBadGateway)
	}
	if !strings.Contains(gotErr.Error(), "empty stream response") {
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

func lastSSEDataPayloadForType(t *testing.T, stream string, eventType string) []byte {
	t.Helper()
	var matched []byte
	for _, line := range strings.Split(stream, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if gjson.Get(payload, "type").String() == eventType {
			matched = []byte(payload)
		}
	}
	if len(matched) == 0 {
		t.Fatalf("SSE event %q not found in stream: %s", eventType, stream)
	}
	return matched
}
