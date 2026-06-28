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

func TestApplyHTTPExtractorStripsMediaAndInjectsVisualContext(t *testing.T) {
	var extractorBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			if strings.HasPrefix(imageSource, "http://") || strings.HasPrefix(imageSource, "https://") {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]any{
						"isError": true,
						"content": []map[string]any{
							{"type": "text", "text": "remote URL should have been downloaded first"},
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
