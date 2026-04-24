package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestOpenAIModelsRewritesGPTImage2ForCherryStudio(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := registry.GetGlobalRegistry()
	clientID := "test-cherry-models"
	reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:          openAIImageModelID,
		Object:      "model",
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "GPT Image 2",
	}})
	defer reg.UnregisterClient(clientID)

	manager := coreauth.NewManager(nil, nil, nil)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.GET("/v1/models", h.OpenAIModels)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("User-Agent", "CherryAI")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"id":"`+openAICherryImageCompatModelID+`"`) {
		t.Fatalf("body = %s, want Cherry compat image model id", body)
	}
	if strings.Contains(body, `"id":"`+openAIImageModelID+`"`) {
		t.Fatalf("body = %s, want original gpt-image-2 id hidden for Cherry model listing", body)
	}
}

func TestOpenAIResponsesCherryCompatAliasStreamsImage(t *testing.T) {
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

	body := `{"model":"` + openAICherryImageCompatModelID + `","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if !strings.Contains(executor.payload, `"model":"`+openAIImageModelID+`"`) {
		t.Fatalf("payload = %s, want alias normalized to gpt-image-2 upstream payload", executor.payload)
	}
	if !strings.Contains(resp.Body.String(), "event: response.completed") {
		t.Fatalf("body = %s, want completed event", resp.Body.String())
	}
}
