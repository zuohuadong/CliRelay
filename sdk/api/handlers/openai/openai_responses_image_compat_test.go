package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestOpenAIResponsesGPTImage2StreamsMarkdownImage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &imageCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Label:    "Team Codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "token", "email": "team@example.com"},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	body := `{"model":"gpt-image-2","input":[{"role":"system","content":"test"},{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"temperature":"[undefined]","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.alt != "images/generations" {
		t.Fatalf("alt = %q, want images/generations", executor.alt)
	}
	if executor.model != "" {
		t.Fatalf("model = %q, want empty route model for direct codex selection", executor.model)
	}
	if !strings.Contains(executor.payload, `"model":"gpt-image-2"`) || !strings.Contains(executor.payload, `"prompt":"test\n\nhi"`) {
		t.Fatalf("payload = %s, want gpt-image-2 prompt converted from responses input", executor.payload)
	}
	responseBody := resp.Body.String()
	if !strings.Contains(responseBody, "event: response.output_text.delta") || !strings.Contains(responseBody, "data:image/png;base64,aGVsbG8=") {
		t.Fatalf("response body = %s, want streamed markdown data image", responseBody)
	}
	if !strings.Contains(responseBody, "event: response.completed") {
		t.Fatalf("response body = %s, want response.completed event", responseBody)
	}
}

func TestOpenAIResponsesGPTImage2ReturnsNonStreamingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &imageCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Label:    "Team Codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "token", "email": "team@example.com"},
	}); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-image-2","input":"draw a fox"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	var parsed struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if parsed.Object != "response" || parsed.Status != "completed" {
		t.Fatalf("response = %#v, want completed response object", parsed)
	}
	if len(parsed.Output) != 1 || len(parsed.Output[0].Content) != 1 || !strings.Contains(parsed.Output[0].Content[0].Text, "data:image/png;base64,aGVsbG8=") {
		t.Fatalf("output = %#v, want markdown data image", parsed.Output)
	}
}
