package openai

import (
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

func TestOpenAIModelsPreserveGPTImage2IDForCherryStudioUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := registry.GetGlobalRegistry()
	clientID := "test-openai-models-preserve-image-id"
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
	if !strings.Contains(body, `"id":"`+openAIImageModelID+`"`) {
		t.Fatalf("body = %s, want original gpt-image-2 id listed", body)
	}
	if strings.Contains(body, `"id":"gptimage-2"`) {
		t.Fatalf("body = %s, want no Cherry-specific alias in model list", body)
	}
}
