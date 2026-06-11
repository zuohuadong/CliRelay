package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestOpenAIModelsClientVersionFiltersByAPIKeyAllowedModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	clientID := "test-openai-models-client-version-filter"
	registry.GetGlobalRegistry().RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{ID: "test-allowed-model"},
		{ID: "test-blocked-model"},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(clientID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIAPIHandler(base)

	router := gin.New()
	router.GET("/v1/models", func(c *gin.Context) {
		c.Set("accessMetadata", map[string]string{"allowed-models": "test-allowed-model"})
		handler.OpenAIModels(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=test", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	var payload struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(payload.Models) != 1 {
		t.Fatalf("models len = %d, want 1; payload=%v", len(payload.Models), payload.Models)
	}
	if payload.Models[0]["slug"] != "test-allowed-model" {
		t.Fatalf("model slug = %v, want test-allowed-model", payload.Models[0]["slug"])
	}
}
