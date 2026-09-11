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
