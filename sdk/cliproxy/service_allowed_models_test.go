package cliproxy

import (
	"context"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestApplyAllowedModelsEmptyKeepsAll(t *testing.T) {
	models := []*ModelInfo{
		{ID: "grok-4.6"},
		{ID: "grok-4.20-reasoning"},
	}
	got := applyAllowedModels(models, nil)
	if len(got) != 2 {
		t.Fatalf("empty allowlist kept %d models, want 2", len(got))
	}
}

func TestApplyAllowedModelsKeepsGrok46And47Family(t *testing.T) {
	models := []*ModelInfo{
		{ID: "grok-4.6"},
		{ID: "grok-4.6-latest"},
		{ID: "grok-4.7"},
		{ID: "grok-4.7-latest"},
		{ID: "grok-4.7-reasoning"},
		{ID: "grok-4.20-reasoning"},
		{ID: "grok-imagine-image-2.0"},
		{ID: "grok-code-fast-1"},
	}
	got := applyAllowedModels(models, []string{"grok-4.6", "grok-4.6-*", "grok-4.7", "grok-4.7-*"})
	ids := modelIDs(got)
	want := []string{"grok-4.6", "grok-4.6-latest", "grok-4.7", "grok-4.7-latest", "grok-4.7-reasoning"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("allowed models = %v, want %v", ids, want)
	}
}

func TestRegisterModelsForAuth_XAIAllowedModelsKeepsGrok46(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthAllowedModels: map[string][]string{
				"xai": {"grok-4.6", "grok-4.6-*", "grok-4.7", "grok-4.7-*"},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-xai-allowlist",
		Provider: "xai",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := registry.GetAvailableModelsByProvider("xai")
	if len(models) == 0 {
		t.Fatal("expected xai models to be registered")
	}

	ids := modelIDs(models)
	for _, id := range ids {
		lower := strings.ToLower(id)
		if lower == "grok-4.6" || strings.HasPrefix(lower, "grok-4.6-") || lower == "grok-4.7" || strings.HasPrefix(lower, "grok-4.7-") {
			continue
		}
		t.Fatalf("unexpected xai model %q remained after allowlist", id)
	}

	seenGrok46 := false
	for _, id := range ids {
		if strings.EqualFold(id, "grok-4.6") {
			seenGrok46 = true
			break
		}
	}
	if !seenGrok46 {
		t.Fatalf("expected grok-4.6 to remain, got %v", ids)
	}
}

func TestRegisterModelsForAuth_XAIAllowedModelsDoesNotRestoreDroppedAliases(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthAllowedModels: map[string][]string{
				"xai": {"grok-4.6", "grok-4.6-*", "grok-4.7", "grok-4.7-*"},
			},
			OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"xai": {{Name: "grok-4.6", Alias: "gpt-5.4", Fork: true}},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-xai-allowlist-aliases",
		Provider: "xai",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}
	coreauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{
		{Name: "grok-4.5", Alias: "grok-4.5-latest"},
		{Name: "grok-4.20-0309-reasoning", Alias: "grok-4.20-reasoning"},
		{Name: "grok-imagine-image-2.0", Alias: "grok-imagine-image-quality-latest"},
	})

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	ids := modelIDs(registry.GetAvailableModelsByProvider("xai"))
	if len(ids) == 0 {
		t.Fatal("expected xai models to be registered")
	}

	seenGrok46 := false
	seenFork := false
	for _, id := range ids {
		lower := strings.ToLower(id)
		switch {
		case lower == "grok-4.6" || strings.HasPrefix(lower, "grok-4.6-") || lower == "grok-4.7" || strings.HasPrefix(lower, "grok-4.7-"):
			if lower == "grok-4.6" {
				seenGrok46 = true
			}
		case lower == "gpt-5.4":
			seenFork = true
		default:
			t.Fatalf("unexpected xai model %q remained after allowlist aliases, got %v", id, ids)
		}
	}
	if !seenGrok46 {
		t.Fatalf("expected grok-4.6 to remain, got %v", ids)
	}
	if !seenFork {
		t.Fatalf("expected grok-4.6 fork gpt-5.4 to remain, got %v", ids)
	}
}

func modelIDs(models []*ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		ids = append(ids, model.ID)
	}
	return ids
}
