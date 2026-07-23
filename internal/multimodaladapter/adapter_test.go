package multimodaladapter

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func boolPtr(v bool) *bool { return &v }

func TestApplyHTTPExtractorOnlyMatchesConcreteRoute(t *testing.T) {
	cfg := config.MultimodalAdaptersConfig{
		Enabled:       boolPtr(true),
		DefaultAction: "extract",
		Rules: []config.MultimodalAdapterRule{
			{
				Name:      "glm-5.1-codex-vision",
				Extractor: "vision",
				Match: config.MultimodalAdapterMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.1"},
				},
			},
		},
		Extractors: []config.MultimodalExtractorConfig{{Name: "vision", Type: "http", Endpoint: "http://127.0.0.1:1"}},
	}
	raw := []byte(`{"model":"gpt-5.3-codex","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`)

	out, report, err := Apply(context.Background(), raw, Route{
		RequestedModel:   "gpt-5.3-codex",
		UpstreamProvider: "codex",
		UpstreamModel:    "gpt-5.5",
		Protocol:         "openai-response",
	}, cfg)
	if err != nil {
		t.Fatalf("Apply error = %v", err)
	}
	if report.Applied {
		t.Fatalf("report.Applied = true, want false")
	}
	if string(out) != string(raw) {
		t.Fatalf("payload changed for unmatched route: %s", out)
	}
}

func TestApplyHTTPExtractorSupportsClaudeBase64AndProtocolInjection(t *testing.T) {
	var extractorBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&extractorBody); err != nil {
			t.Fatalf("decode extractor body: %v", err)
		}
		_, _ = w.Write([]byte(`{"text":"Claude image shows E_CONN_RESET."}`))
	}))
	defer server.Close()
	cfg := config.MultimodalAdaptersConfig{
		Enabled:       boolPtr(true),
		DefaultAction: "extract",
		InjectAs:      "visual_context",
		Rules: []config.MultimodalAdapterRule{{
			Extractor: "vision",
			Match:     config.MultimodalAdapterMatch{Protocols: []string{"claude"}},
		}},
		Extractors: []config.MultimodalExtractorConfig{{Name: "vision", Type: "http", Endpoint: server.URL}},
	}
	raw := []byte(`{"model":"claude-sonnet","system":"follow the user","messages":[` +
		`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"ZmFrZQ=="}},{"type":"text","text":"inspect this"}]}]}`)
	out, report, err := Apply(context.Background(), raw, Route{Protocol: "claude"}, cfg)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if report.MediaItems != 1 || !report.Applied {
		t.Fatalf("report = %#v", report)
	}
	body := string(out)
	if strings.Contains(body, "ZmFrZQ==") || !strings.Contains(body, "E_CONN_RESET") {
		t.Fatalf("Claude media was not replaced: %s", body)
	}
	if strings.Contains(body, `"role":"system"`) {
		t.Fatalf("Claude messages received an invalid system role: %s", body)
	}
	if !strings.Contains(string(extractorBody["media"].([]any)[0].(map[string]any)["url"].(string)), "data:image/png") {
		t.Fatalf("Claude image was not forwarded as a data URL: %#v", extractorBody)
	}
}

func TestApplyHTTPExtractorSupportsGeminiInlineDataAndContentsInjection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"Gemini image shows a broken chart."}`))
	}))
	defer server.Close()
	cfg := config.MultimodalAdaptersConfig{
		Enabled:       boolPtr(true),
		DefaultAction: "extract",
		Rules:         []config.MultimodalAdapterRule{{Extractor: "vision", Match: config.MultimodalAdapterMatch{Protocols: []string{"gemini"}}}},
		Extractors:    []config.MultimodalExtractorConfig{{Name: "vision", Type: "http", Endpoint: server.URL}},
	}
	raw := []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"ZmFrZQ=="}},{"text":"inspect"}]}]}`)
	out, _, err := Apply(context.Background(), raw, Route{Protocol: "gemini"}, cfg)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	body := string(out)
	if strings.Contains(body, "inlineData") || !strings.Contains(body, "broken chart") || !strings.Contains(body, `"contents"`) {
		t.Fatalf("Gemini media/injection shape is wrong: %s", body)
	}
}

func TestApplyHTTPExtractorReplacesMediaInOriginalPosition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"visual evidence"}`))
	}))
	defer server.Close()
	cfg := config.MultimodalAdaptersConfig{
		Enabled:       boolPtr(true),
		DefaultAction: "extract",
		InjectAs:      "visual_context",
		Rules:         []config.MultimodalAdapterRule{{Extractor: "vision"}},
		Extractors:    []config.MultimodalExtractorConfig{{Name: "vision", Type: "http", Endpoint: server.URL}},
	}
	cases := []struct {
		name     string
		protocol string
		raw      string
		texts    func(map[string]any) []string
	}{
		{
			name:     "responses",
			protocol: "openai-response",
			raw:      `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"before"},{"type":"input_image","image_url":"https://example.com/one.png"},{"type":"input_text","text":"after"}]}]}`,
			texts: func(root map[string]any) []string {
				return responseContentTexts(root, "input")
			},
		},
		{
			name:     "chat",
			protocol: "chat",
			raw:      `{"messages":[{"role":"user","content":[{"type":"text","text":"before"},{"type":"image_url","image_url":{"url":"https://example.com/one.png"}},{"type":"text","text":"after"}]}]}`,
			texts: func(root map[string]any) []string {
				return responseContentTexts(root, "messages")
			},
		},
		{
			name:     "claude",
			protocol: "claude",
			raw:      `{"messages":[{"role":"user","content":[{"type":"text","text":"before"},{"type":"image","source":{"type":"url","url":"https://example.com/one.png"}},{"type":"text","text":"after"}]}]}`,
			texts: func(root map[string]any) []string {
				return responseContentTexts(root, "messages")
			},
		},
		{
			name:     "gemini",
			protocol: "gemini",
			raw:      `{"contents":[{"role":"user","parts":[{"text":"before"},{"inlineData":{"mimeType":"image/png","data":"ZmFrZQ=="}},{"text":"after"}]}]}`,
			texts: func(root map[string]any) []string {
				return geminiPartTexts(root)
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			out, _, err := Apply(context.Background(), []byte(testCase.raw), Route{Protocol: testCase.protocol}, cfg)
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			var root map[string]any
			if err := json.Unmarshal(out, &root); err != nil {
				t.Fatalf("output JSON: %v", err)
			}
			texts := testCase.texts(root)
			if len(texts) != 3 || texts[0] != "before" || texts[2] != "after" || !strings.Contains(texts[1], "visual evidence") {
				t.Fatalf("media replacement moved or lost its position: %#v", root)
			}
		})
	}
}

func responseContentTexts(root map[string]any, field string) []string {
	historyEntries, _ := root[field].([]any)
	if len(historyEntries) == 0 {
		return nil
	}
	message, _ := historyEntries[0].(map[string]any)
	contentParts, _ := message["content"].([]any)
	texts := make([]string, 0, len(contentParts))
	for _, contentPart := range contentParts {
		partFields, _ := contentPart.(map[string]any)
		texts = append(texts, stringField(partFields, "text"))
	}
	return texts
}

func geminiPartTexts(root map[string]any) []string {
	contentEntries, _ := root["contents"].([]any)
	if len(contentEntries) == 0 {
		return nil
	}
	content, _ := contentEntries[0].(map[string]any)
	contentParts, _ := content["parts"].([]any)
	texts := make([]string, 0, len(contentParts))
	for _, contentPart := range contentParts {
		partFields, _ := contentPart.(map[string]any)
		texts = append(texts, stringField(partFields, "text"))
	}
	return texts
}

func stringField(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return value
}

func TestApplyHTTPExtractorStripsMediaAndInjectsVisualContext(t *testing.T) {
	var extractorBody map[string]any
	extractorCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extractorCalls++
		if err := json.NewDecoder(r.Body).Decode(&extractorBody); err != nil {
			t.Fatalf("decode extractor body: %v", err)
		}
		_, _ = w.Write([]byte(`{"text":"The screenshot shows a terminal error: build failed."}`))
	}))
	defer server.Close()

	cfg := config.MultimodalAdaptersConfig{
		Enabled:       boolPtr(true),
		DefaultAction: "extract",
		InjectAs:      "visual_context",
		Rules: []config.MultimodalAdapterRule{
			{
				Name:      "glm-5.1-codex-vision",
				Extractor: "vision",
				Match: config.MultimodalAdapterMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.1"},
				},
			},
		},
		Extractors: []config.MultimodalExtractorConfig{{Name: "vision", Type: "http", Endpoint: server.URL}},
	}
	raw := []byte(`{"model":"gpt-5.3-codex","input":[{"role":"user","content":[{"type":"input_text","text":"what failed?"},{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`)

	out, report, err := Apply(context.Background(), raw, Route{
		RequestedModel:   "gpt-5.3-codex",
		UpstreamProvider: "bigmodel-coding",
		UpstreamModel:    "glm-5.1",
		Protocol:         "openai-response",
	}, cfg)
	if err != nil {
		t.Fatalf("Apply error = %v", err)
	}
	if !report.Applied || !report.Stripped || !report.Injected || report.MediaItems != 1 || report.Extractor != "vision" {
		t.Fatalf("report = %#v", report)
	}
	body := string(out)
	if strings.Contains(body, "input_image") || strings.Contains(body, "image_url") {
		t.Fatalf("media was not stripped: %s", body)
	}
	if !strings.Contains(body, "visual_context") || !strings.Contains(body, "terminal error") {
		t.Fatalf("visual context was not injected: %s", body)
	}
	media, _ := extractorBody["media"].([]any)
	if len(media) != 1 {
		t.Fatalf("extractor media = %#v, want one item", extractorBody["media"])
	}
	if _, _, err = Apply(context.Background(), raw, Route{
		RequestedModel:   "gpt-5.3-codex",
		UpstreamProvider: "bigmodel-coding",
		UpstreamModel:    "glm-5.1",
		Protocol:         "openai-response",
	}, cfg); err != nil {
		t.Fatalf("cached Apply error = %v", err)
	}
	if extractorCalls != 1 {
		t.Fatalf("extractor calls = %d, want one cached call", extractorCalls)
	}
}

func TestApplyZAIVisionHTTPExtractorStripsMediaAndInjectsVisualContext(t *testing.T) {
	var extractorBody map[string]any
	var gotPath string
	var gotAuth string
	var gotSessionID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotSessionID = r.Header.Get("Session_id")
		if err := json.NewDecoder(r.Body).Decode(&extractorBody); err != nil {
			t.Fatalf("decode extractor body: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"The screenshot shows error code E_CONN_RESET."}}]}`))
	}))
	defer server.Close()

	cfg := config.MultimodalAdaptersConfig{
		Enabled:       boolPtr(true),
		DefaultAction: "extract",
		InjectAs:      "visual_context",
		Rules: []config.MultimodalAdapterRule{
			{
				Name:      "glm-5.1-codex-vision",
				Extractor: "zai-vision-http",
				Match: config.MultimodalAdapterMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.1"},
				},
			},
		},
		Extractors: []config.MultimodalExtractorConfig{
			{
				Name:     "zai-vision-http",
				Type:     "zai-vision-http",
				Endpoint: server.URL + "/api/paas/v4",
				ToolName: "glm-4.5v",
				Headers:  map[string]string{"Authorization": "Bearer sk-test"},
				Env:      map[string]string{"identity_fingerprint": "codex"},
			},
		},
	}
	raw := []byte(`{"model":"gpt-5.3-codex","input":[{"role":"user","content":[{"type":"input_text","text":"what failed?"},{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`)

	out, report, err := Apply(context.Background(), raw, Route{
		RequestedModel:   "gpt-5.3-codex",
		UpstreamProvider: "bigmodel-coding",
		UpstreamModel:    "glm-5.1",
		Protocol:         "openai-response",
	}, cfg)
	if err != nil {
		t.Fatalf("Apply error = %v", err)
	}
	if gotPath != "/api/paas/v4/chat/completions" {
		t.Fatalf("path = %q, want /api/paas/v4/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if strings.TrimSpace(gotSessionID) == "" {
		t.Fatalf("Session_id should be generated")
	}
	if extractorBody["model"] != "glm-4.5v" {
		t.Fatalf("model = %#v", extractorBody["model"])
	}
	if extractorBody["max_tokens"] != float64(512) {
		t.Fatalf("max_tokens = %#v", extractorBody["max_tokens"])
	}
	thinking, _ := extractorBody["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v", extractorBody["thinking"])
	}
	if !report.Applied || !report.Stripped || !report.Injected || report.MediaItems != 1 || report.Extractor != "zai-vision-http" {
		t.Fatalf("report = %#v", report)
	}
	body := string(out)
	if strings.Contains(body, "input_image") || strings.Contains(body, "image_url") {
		t.Fatalf("media was not stripped: %s", body)
	}
	if !strings.Contains(body, "visual_context") || !strings.Contains(body, "E_CONN_RESET") {
		t.Fatalf("visual context was not injected: %s", body)
	}
}

func TestApplyMCPJSONLExtractorStripsMediaAndInjectsVisualContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake png body"))
	}))
	defer server.Close()

	cfg := config.MultimodalAdaptersConfig{
		Enabled:       boolPtr(true),
		DefaultAction: "extract",
		InjectAs:      "visual_context",
		Rules: []config.MultimodalAdapterRule{
			{
				Name:      "glm-5.2-codex-vision",
				Extractor: "zai-vision-mcp",
				Match: config.MultimodalAdapterMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.2"},
				},
			},
		},
		Extractors: []config.MultimodalExtractorConfig{
			{
				Name:     "zai-vision-mcp",
				Type:     "mcp",
				Command:  os.Args[0],
				Args:     []string{"-test.run=TestHelperProcessMCPJSONL"},
				ToolName: "analyze_image",
				Prompt:   "Describe the image.",
				Env: map[string]string{
					"CLIRELAY_MCP_JSONL_HELPER": "1",
					"MCP_TRANSPORT":             "jsonl",
				},
			},
		},
	}
	raw := []byte(`{"model":"gpt-5.3-codex","input":[{"role":"user","content":[{"type":"input_text","text":"what is shown?"},{"type":"input_image","image_url":"` + server.URL + `/a.png"}]}]}`)

	out, report, err := Apply(context.Background(), raw, Route{
		RequestedModel:   "gpt-5.3-codex",
		UpstreamProvider: "bigmodel-coding",
		UpstreamModel:    "glm-5.2",
		Protocol:         "openai-response",
	}, cfg)
	if err != nil {
		t.Fatalf("Apply error = %v", err)
	}
	if !report.Applied || !report.Stripped || !report.Injected || report.MediaItems != 1 || report.Extractor != "zai-vision-mcp" {
		t.Fatalf("report = %#v", report)
	}
	body := string(out)
	if strings.Contains(body, "input_image") || strings.Contains(body, "image_url") {
		t.Fatalf("media was not stripped: %s", body)
	}
	if !strings.Contains(body, "visual_context") || !strings.Contains(body, "MCP analyzer saw a pink pig") {
		t.Fatalf("visual context was not injected: %s", body)
	}
}

func TestApplyMCPJSONLExtractorWritesDataURLToTempFile(t *testing.T) {
	cfg := config.MultimodalAdaptersConfig{
		Enabled:       boolPtr(true),
		DefaultAction: "extract",
		InjectAs:      "visual_context",
		Rules: []config.MultimodalAdapterRule{
			{
				Name:      "glm-5.2-codex-vision",
				Extractor: "zai-vision-mcp",
				Match: config.MultimodalAdapterMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.2"},
				},
			},
		},
		Extractors: []config.MultimodalExtractorConfig{
			{
				Name:     "zai-vision-mcp",
				Type:     "mcp",
				Command:  os.Args[0],
				Args:     []string{"-test.run=TestHelperProcessMCPJSONL"},
				ToolName: "analyze_image",
				Prompt:   "Describe the image.",
				Env: map[string]string{
					"CLIRELAY_MCP_JSONL_HELPER": "1",
					"MCP_TRANSPORT":             "jsonl",
				},
			},
		},
	}
	raw := []byte(`{"model":"gpt-5.3-codex","input":[{"role":"user","content":[{"type":"input_text","text":"what is shown?"},{"type":"input_image","image_url":"data:image/png;base64,ZmFrZSBwbmcgYm9keQ=="}]}]}`)

	out, report, err := Apply(context.Background(), raw, Route{
		RequestedModel:   "gpt-5.3-codex",
		UpstreamProvider: "bigmodel-coding",
		UpstreamModel:    "glm-5.2",
		Protocol:         "openai-response",
	}, cfg)
	if err != nil {
		t.Fatalf("Apply error = %v", err)
	}
	if !report.Applied || !report.Stripped || !report.Injected || report.MediaItems != 1 || report.Extractor != "zai-vision-mcp" {
		t.Fatalf("report = %#v", report)
	}
	body := string(out)
	if strings.Contains(body, "input_image") || strings.Contains(body, "data:image") {
		t.Fatalf("media data URL was not stripped from output: %s", body)
	}
	if !strings.Contains(body, "visual_context") || !strings.Contains(body, "MCP analyzer saw a pink pig") {
		t.Fatalf("visual context was not injected: %s", body)
	}
}

func TestHelperProcessMCPJSONL(t *testing.T) {
	if os.Getenv("CLIRELAY_MCP_JSONL_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		id, hasID := msg["id"]
		if !hasID {
			continue
		}
		method, _ := msg["method"].(string)
		switch method {
		case "initialize":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "jsonl-helper", "version": "test"},
				},
			})
		case "tools/list":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name": "analyze_image",
							"inputSchema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"image_source": map[string]any{"type": "string"},
									"prompt":       map[string]any{"type": "string"},
								},
								"required": []string{"image_source", "prompt"},
							},
						},
					},
				},
			})
		case "tools/call":
			params, _ := msg["params"].(map[string]any)
			arguments, _ := params["arguments"].(map[string]any)
			imageSource, _ := arguments["image_source"].(string)
			if strings.HasPrefix(imageSource, "http://") || strings.HasPrefix(imageSource, "https://") || strings.HasPrefix(imageSource, "data:") {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]any{
						"isError": true,
						"content": []map[string]any{
							{"type": "text", "text": "media URL should have been prepared as a local file first"},
						},
					},
				})
				continue
			}
			if _, err := os.Stat(imageSource); err != nil {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]any{
						"isError": true,
						"content": []map[string]any{
							{"type": "text", "text": "prepared media file does not exist"},
						},
					},
				})
				continue
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "MCP analyzer saw a pink pig face."},
					},
				},
			})
		}
	}
	os.Exit(0)
}
