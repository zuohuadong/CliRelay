package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
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
		t.Fatalf("models len = %d, want 1; payload=%v", len(payload.Models), payload)
	}
	if payload.Models[0]["slug"] != "test-allowed-model" {
		t.Fatalf("model slug = %v, want test-allowed-model", payload.Models[0]["slug"])
	}
}

func TestOpenAIModelsFiltersByAllowedChannelGroupModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	clientID := "test-openai-models-channel-group-filter"
	registry.GetGlobalRegistry().RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{ID: "scoped-allowed-model"},
		{ID: "scoped-blocked-model"},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(clientID)

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			ChannelGroups: []internalconfig.RoutingChannelGroup{
				{
					Name:          "lynn",
					Match:         internalconfig.ChannelGroupMatch{Tags: []string{"openai"}},
					AllowedModels: []string{"scoped-allowed-model"},
				},
			},
		},
	})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       clientID,
		Provider: "openai",
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIAPIHandler(base)

	tests := []struct {
		name     string
		query    string
		metadata map[string]string
	}{
		{
			name:     "openai-group-only",
			query:    "",
			metadata: map[string]string{"allowed-channel-groups": "lynn"},
		},
		{
			name:     "codex-client-group-only",
			query:    "?client_version=test",
			metadata: map[string]string{"allowed-channel-groups": "lynn"},
		},
		{
			name:  "openai-api-key-and-group-intersection",
			query: "",
			metadata: map[string]string{
				"allowed-models":         "scoped-allowed-model,scoped-blocked-model",
				"allowed-channel-groups": "lynn",
			},
		},
		{
			name:  "codex-client-api-key-and-group-intersection",
			query: "?client_version=test",
			metadata: map[string]string{
				"allowed-models":         "scoped-allowed-model,scoped-blocked-model",
				"allowed-channel-groups": "lynn",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/v1/models", func(c *gin.Context) {
				c.Set("accessMetadata", tt.metadata)
				handler.OpenAIModels(c)
			})

			req := httptest.NewRequest(http.MethodGet, "/v1/models"+tt.query, nil)
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)

			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
			}

			var modelIDs []string
			if tt.query == "" {
				var payload struct {
					Models []struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				for _, model := range payload.Models {
					modelIDs = append(modelIDs, model.ID)
				}
			} else {
				var payload struct {
					Models []struct {
						Slug string `json:"slug"`
					} `json:"models"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				for _, model := range payload.Models {
					modelIDs = append(modelIDs, model.Slug)
				}
			}

			if len(modelIDs) != 1 || modelIDs[0] != "scoped-allowed-model" {
				t.Fatalf("models = %v, want [scoped-allowed-model]", modelIDs)
			}
		})
	}
}

func TestOpenAIModelsFiltersByAllowedChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	allowedClientID := "test-openai-models-allowed-channel"
	blockedClientID := "test-openai-models-blocked-channel"
	registry.GetGlobalRegistry().RegisterClient(allowedClientID, "openai", []*registry.ModelInfo{
		{ID: "channel-allowed-model"},
	})
	registry.GetGlobalRegistry().RegisterClient(blockedClientID, "openai", []*registry.ModelInfo{
		{ID: "channel-blocked-model"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(allowedClientID)
		registry.GetGlobalRegistry().UnregisterClient(blockedClientID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	for _, auth := range []*coreauth.Auth{
		{ID: allowedClientID, Provider: "openai", Label: "allowed-channel"},
		{ID: blockedClientID, Provider: "openai", Label: "blocked-channel"},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}

	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.GET("/v1/models", func(c *gin.Context) {
		c.Set("accessMetadata", map[string]string{"allowed-channels": "allowed-channel"})
		handler.OpenAIModels(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	var payload struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Models) != 1 || payload.Models[0].ID != "channel-allowed-model" {
		t.Fatalf("models = %v, want [channel-allowed-model]", payload.Models)
	}
}

func TestOpenAIModelsClientVersionHidesOpenAIVideoModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	clientID := "test-openai-models-client-version-video"
	registry.GetGlobalRegistry().RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{
		{ID: "agnes-video-v2.0", Object: "model", OwnedBy: "agnes", Type: registry.OpenAIVideoModelType},
		{ID: "agnes-2.0-flash", Object: "model", OwnedBy: "agnes", Type: "openai-compatibility"},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(clientID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIAPIHandler(base)

	router := gin.New()
	router.GET("/v1/models", handler.OpenAIModels)

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

	seenChat := false
	for _, model := range payload.Models {
		if model["slug"] == "agnes-video-v2.0" && model["visibility"] != "hide" {
			t.Fatalf("agnes-video-v2.0 must be hidden from Codex chat model list: %v", model)
		}
		if model["slug"] == "agnes-2.0-flash" {
			seenChat = true
		}
	}
	if !seenChat {
		t.Fatalf("expected chat model to remain visible; payload=%v", payload.Models)
	}
}

func TestSanitizeChatCompletionsToolMessageHistoryDropsUnpairedItems(t *testing.T) {
	raw := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"start"},{"role":"tool","tool_call_id":"call-orphan","content":"missing call"},{"role":"assistant","tool_calls":[{"id":"call-unanswered","type":"function","function":{"name":"exec_command","arguments":"{}"}}]},{"role":"assistant","tool_calls":[{"id":"call-ok","type":"function","function":{"name":"exec_command","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-ok","content":"done"},{"role":"user","content":"next"}]}`)

	sanitized := sanitizeChatCompletionsToolMessageHistory(raw)

	messages := gjson.GetBytes(sanitized, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages len = %d, want 4: %s", len(messages), sanitized)
	}
	if messages[0].Get("role").String() != "user" || messages[1].Get("tool_calls.0.id").String() != "call-ok" || messages[2].Get("tool_call_id").String() != "call-ok" || messages[3].Get("role").String() != "user" {
		t.Fatalf("unexpected sanitized messages: %s", sanitized)
	}
	if strings.Contains(string(sanitized), "call-orphan") || strings.Contains(string(sanitized), "call-unanswered") {
		t.Fatalf("unpaired tool history leaked through: %s", sanitized)
	}
}

func TestSanitizeChatCompletionsToolMessageHistoryNormalizesCallID(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"exec_command","arguments":"{}"}}]},{"role":"tool","call_id":"call-1","content":"done"}]}`)

	sanitized := sanitizeChatCompletionsToolMessageHistory(raw)

	if got := gjson.GetBytes(sanitized, "messages.1.tool_call_id").String(); got != "call-1" {
		t.Fatalf("tool_call_id = %q, want call-1: %s", got, sanitized)
	}
	if got := len(gjson.GetBytes(sanitized, "messages").Array()); got != 2 {
		t.Fatalf("messages len = %d, want 2: %s", got, sanitized)
	}
}
