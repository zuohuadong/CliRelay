package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuth_UsesPreMergedExcludedModelsAttribute(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"gemini": {"gemini-2.5-pro"},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-gemini",
		Provider: "gemini",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":       "oauth",
			"excluded_models": "gemini-2.5-flash",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := registry.GetAvailableModelsByProvider("gemini")
	if len(models) == 0 {
		t.Fatal("expected gemini models to be registered")
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if strings.EqualFold(modelID, "gemini-2.5-flash") {
			t.Fatalf("expected model %q to be excluded by auth attribute", modelID)
		}
	}

	seenGlobalExcluded := false
	for _, model := range models {
		if model == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(model.ID), "gemini-2.5-pro") {
			seenGlobalExcluded = true
			break
		}
	}
	if !seenGlobalExcluded {
		t.Fatal("expected global excluded model to be present when attribute override is set")
	}
}

func TestRegisterModelsForAuth_OpenAICompatibilityImageModelType(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "images",
					BaseURL: "https://example.com/v1",
					Models: []config.OpenAICompatibilityModel{
						{Name: "upstream-image", Alias: "compat-image", Image: true},
						{Name: "upstream-video", Alias: "compat-video", Video: true},
						{Name: "upstream-chat", Alias: "compat-chat"},
					},
				},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-openai-compat-image",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":    "api_key",
			"compat_name":  "images",
			"provider_key": "images",
		},
	}

	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := modelRegistry.GetModelsForClient(auth.ID)
	var imageModel *internalregistry.ModelInfo
	var videoModel *internalregistry.ModelInfo
	var chatModel *internalregistry.ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch strings.TrimSpace(model.ID) {
		case "compat-image":
			imageModel = model
		case "compat-video":
			videoModel = model
		case "compat-chat":
			chatModel = model
		}
	}
	if imageModel == nil {
		t.Fatal("expected compat-image to be registered")
	}
	if imageModel.Type != internalregistry.OpenAIImageModelType {
		t.Fatalf("image model type = %q, want %q", imageModel.Type, internalregistry.OpenAIImageModelType)
	}
	if imageModel.Thinking != nil {
		t.Fatalf("image model thinking = %+v, want nil", imageModel.Thinking)
	}
	if videoModel == nil {
		t.Fatal("expected compat-video to be registered")
	}
	if videoModel.Type != internalregistry.OpenAIVideoModelType {
		t.Fatalf("video model type = %q, want %q", videoModel.Type, internalregistry.OpenAIVideoModelType)
	}
	if videoModel.Thinking != nil {
		t.Fatalf("video model thinking = %+v, want nil", videoModel.Thinking)
	}
	if chatModel == nil {
		t.Fatal("expected compat-chat to be registered")
	}
	if chatModel.Type != "openai-compatibility" {
		t.Fatalf("chat model type = %q, want openai-compatibility", chatModel.Type)
	}
	if chatModel.Thinking == nil {
		t.Fatal("expected chat model to keep default thinking support")
	}
}

func TestRegisterModelsForAuth_OpenAICompatibilityMetadataModels(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-openai-compat-metadata",
		Provider: "openai-compatibility",
		Label:    "agnes",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":    "api_key",
			"compat_name":  "agnes",
			"provider_key": "agnes",
			"base_url":     "https://example.com/v1",
			"api_key":      "sk-test",
		},
		Metadata: map[string]any{
			"models": []any{
				map[string]any{"name": "agnes-2.0-flash", "alias": "agnes-2.0-flash"},
				map[string]any{"name": "agnes-image-2.1-flash", "alias": "agnes-image-2.1-flash", "image": true},
				map[string]any{"name": "agnes-video-v2.0", "alias": "agnes-video-v2.0", "video": true},
			},
		},
	}

	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := modelRegistry.GetModelsForClient(auth.ID)
	if len(models) != 3 {
		t.Fatalf("models len = %d, want 3: %#v", len(models), models)
	}
	typesByID := map[string]string{}
	for _, model := range models {
		if model != nil {
			typesByID[strings.TrimSpace(model.ID)] = model.Type
		}
	}
	if typesByID["agnes-2.0-flash"] != "openai-compatibility" {
		t.Fatalf("chat model type = %q, want openai-compatibility", typesByID["agnes-2.0-flash"])
	}
	if typesByID["agnes-image-2.1-flash"] != internalregistry.OpenAIImageModelType {
		t.Fatalf("image model type = %q, want %q", typesByID["agnes-image-2.1-flash"], internalregistry.OpenAIImageModelType)
	}
	if typesByID["agnes-video-v2.0"] != internalregistry.OpenAIVideoModelType {
		t.Fatalf("video model type = %q, want %q", typesByID["agnes-video-v2.0"], internalregistry.OpenAIVideoModelType)
	}
}

func TestRegisterModelsForAuth_AntigravityFetchesWebSearchCapability(t *testing.T) {
	var sawFetch bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != antigravityModelsPath {
			t.Fatalf("path = %q, want %s", r.URL.Path, antigravityModelsPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		sawFetch = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
				"models": {
					"gemini-3.1-flash-lite": {
						"displayName": "Gemini 3.1 Flash Lite",
						"maxTokens": 1,
						"maxOutputTokens": 2
					},
					"gemini-3.1-pro-high": {
						"displayName": "Gemini 3.1 Pro (High)",
						"maxTokens": 1048576,
						"maxOutputTokens": 65535
					},
					"fetched-only-search-model": {
						"displayName": "Fetched Only Search Model"
					}
				},
				"webSearchModelIds": ["gemini-3.1-flash-lite", "gemini-3.1-pro-high", "fetched-only-search-model"]
			}`))
	}))
	defer server.Close()

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-antigravity-fetch-models",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
		},
	}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)
	if !sawFetch {
		t.Fatal("expected fetchAvailableModels request")
	}

	models := registry.GetModelsForClient(auth.ID)
	staticModels := internalregistry.GetAntigravityModels()
	staticByID := make(map[string]*internalregistry.ModelInfo, len(staticModels))
	for _, model := range staticModels {
		if model != nil {
			staticByID[model.ID] = model
		}
	}

	var webSearchModel, highModel, agentModel, staticOnlyModel, fetchedOnlyModel *internalregistry.ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch strings.TrimSpace(model.ID) {
		case "gemini-3.1-flash-lite":
			webSearchModel = model
		case "gemini-3.1-pro-high":
			highModel = model
		case "gemini-3-flash-agent":
			agentModel = model
		case "gpt-oss-120b-medium":
			staticOnlyModel = model
		case "fetched-only-search-model":
			fetchedOnlyModel = model
		}
	}
	if webSearchModel == nil {
		t.Fatal("expected gemini-3.1-flash-lite to be registered")
	}
	if !webSearchModel.SupportsWebSearch {
		t.Fatal("expected gemini-3.1-flash-lite to support web search")
	}
	staticWebSearchModel := staticByID["gemini-3.1-flash-lite"]
	if staticWebSearchModel == nil {
		t.Fatal("expected static gemini-3.1-flash-lite definition")
	}
	if webSearchModel.ContextLength != staticWebSearchModel.ContextLength || webSearchModel.MaxCompletionTokens != staticWebSearchModel.MaxCompletionTokens {
		t.Fatalf("static token limits should be preserved, got=%#v static=%#v", webSearchModel, staticWebSearchModel)
	}
	if highModel == nil {
		t.Fatal("expected fetched gemini-3.1-pro-high to be registered")
	}
	if !highModel.SupportsWebSearch {
		t.Fatal("expected fetched gemini-3.1-pro-high to support web search")
	}
	if agentModel == nil {
		t.Fatal("expected gemini-3-flash-agent to be registered")
	}
	if agentModel.SupportsWebSearch {
		t.Fatal("gemini-3-flash-agent should not support web search")
	}
	if staticOnlyModel == nil {
		t.Fatal("expected static-only Antigravity model to remain registered")
	}
	if fetchedOnlyModel != nil {
		t.Fatalf("fetched-only model should not be registered: %#v", fetchedOnlyModel)
	}
}
