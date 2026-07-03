package cliproxy

import (
	"context"
	"strings"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuth_OpenCodeGoRegistersDefaultModels(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "opencode-go-default-models",
		Provider: "opencode-go",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"api_key":   "go-key",
		},
	}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := registry.GetModelsForClient(auth.ID)
	if len(models) != 20 {
		t.Fatalf("registered OpenCode Go models = %d, want 20: %+v", len(models), models)
	}
	if !opencodeGoHasModelID(models, "kimi-k2.7-code") {
		t.Fatalf("kimi-k2.7-code not registered; got %+v", models)
	}
	if !opencodeGoHasModelID(models, "qwen3.7-max") {
		t.Fatalf("qwen3.7-max not registered; got %+v", models)
	}
}

func TestRegisterModelsForAuth_OpenCodeGoUsesExplicitModels(t *testing.T) {
	service := &Service{cfg: &config.Config{
		OpenCodeGoKey: []config.OpenCodeGoKey{{
			APIKey: "go-key-explicit",
			Models: []config.OpenCodeGoModel{
				{Name: "qwen3.7-max"},
				{Name: "custom-opencode-model", Alias: "custom-alias"},
			},
		}},
	}}
	auth := &coreauth.Auth{
		ID:       "opencode-go-explicit-models",
		Provider: "opencode-go",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"api_key":   "go-key-explicit",
		},
	}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := registry.GetModelsForClient(auth.ID)
	if len(models) != 2 {
		t.Fatalf("registered OpenCode Go explicit models = %d, want 2: %+v", len(models), models)
	}
	if !opencodeGoHasModelID(models, "qwen3.7-max") {
		t.Fatalf("qwen3.7-max not registered; got %+v", models)
	}
	if !opencodeGoHasModelID(models, "custom-alias") {
		t.Fatalf("custom-alias not registered; got %+v", models)
	}
	if opencodeGoHasModelID(models, "deepseek-v4-flash") {
		t.Fatalf("deepseek-v4-flash should not be registered when explicit models are configured; got %+v", models)
	}
}

func opencodeGoHasModelID(models []*ModelInfo, want string) bool {
	for _, model := range models {
		if model != nil && strings.EqualFold(strings.TrimSpace(model.ID), want) {
			return true
		}
	}
	return false
}
