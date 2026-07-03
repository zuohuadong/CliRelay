package cliproxy

import (
	"context"
	"strings"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuth_BedrockRegistersDefaultModels(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "bedrock-default-models",
		Provider: "bedrock",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":     "api_key",
			"api_key":       "bedrock-key",
			"access_key_id": "bedrock-access",
			"region":        "us-east-1",
		},
	}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := registry.GetModelsForClient(auth.ID)
	if len(models) == 0 {
		t.Fatal("expected Bedrock models to be registered")
	}
	if !bedrockHasModelID(models, "claude-sonnet-4-5") {
		t.Fatalf("claude-sonnet-4-5 not registered; got %+v", models)
	}
}

func TestRegisterModelsForAuth_BedrockUsesExplicitModels(t *testing.T) {
	service := &Service{cfg: &config.Config{
		BedrockKey: []config.BedrockKey{{
			AuthMode: "api-key",
			APIKey:   "bedrock-key-explicit",
			Region:   "us-east-1",
			Models: []config.BedrockModel{
				{Name: "anthropic.claude-3-5-sonnet-20240620-v1:0", Alias: "custom-bedrock-sonnet"},
			},
			ExcludedModels: []string{"custom-excluded"},
		}},
	}}
	auth := &coreauth.Auth{
		ID:       "bedrock-explicit-models",
		Provider: "bedrock",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "api_key",
			"api_key":   "bedrock-key-explicit",
			"region":    "us-east-1",
		},
	}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := registry.GetModelsForClient(auth.ID)
	if len(models) != 1 {
		t.Fatalf("registered Bedrock explicit models = %d, want 1: %+v", len(models), models)
	}
	if !bedrockHasModelID(models, "custom-bedrock-sonnet") {
		t.Fatalf("custom-bedrock-sonnet not registered; got %+v", models)
	}
	if bedrockHasModelID(models, "claude-sonnet-4-5") {
		t.Fatalf("default model should not be registered when explicit models are configured; got %+v", models)
	}
}

func bedrockHasModelID(models []*ModelInfo, want string) bool {
	for _, model := range models {
		if model != nil && strings.EqualFold(strings.TrimSpace(model.ID), want) {
			return true
		}
	}
	return false
}
