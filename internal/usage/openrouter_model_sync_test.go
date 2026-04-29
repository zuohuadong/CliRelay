package usage

import (
	"context"
	"testing"
)

func TestSyncOpenRouterModelsAddsNewModelsWithLocalModelIDPricingAndOwner(t *testing.T) {
	initModelConfigTestDB(t)

	result, err := SyncOpenRouterModelList(context.Background(), []OpenRouterRemoteModel{
		{
			ID:          "openai/gpt-openrouter-test",
			Name:        "OpenAI: GPT OpenRouter Test",
			Description: "Agentic test model",
			Pricing: OpenRouterRemotePricing{
				Prompt:         "0.00000175",
				Completion:     "0.000014",
				InputCacheRead: "0.000000175",
			},
		},
	})
	if err != nil {
		t.Fatalf("SyncOpenRouterModelList() error = %v", err)
	}
	if result.Seen != 1 || result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	model, ok := GetModelConfig("gpt-openrouter-test")
	if !ok {
		t.Fatal("expected gpt-openrouter-test to be imported")
	}
	if _, ok := GetModelConfig("openai/gpt-openrouter-test"); ok {
		t.Fatal("did not expect OpenRouter provider prefix to be stored in model id")
	}
	if model.OwnedBy != "openai" || model.Source != "openrouter" || model.Description != "Agentic test model" {
		t.Fatalf("unexpected imported model metadata: %+v", model)
	}
	if model.InputPricePerMillion != 1.75 || model.OutputPricePerMillion != 14 || model.CachedPricePerMillion != 0.175 {
		t.Fatalf("unexpected imported model pricing: %+v", model)
	}
	if _, ok := GetModelOwnerPreset("openai"); !ok {
		t.Fatal("expected openai owner preset to exist")
	}
}

func TestSyncOpenRouterModelsUpdatesExistingUserModelPricingOnly(t *testing.T) {
	initModelConfigTestDB(t)

	if err := UpsertModelConfig(ModelConfigRow{
		ModelID:               "gpt-openrouter-test",
		OwnedBy:               "custom-owner",
		Description:           "Local override",
		Enabled:               true,
		PricingMode:           "token",
		InputPricePerMillion:  9,
		OutputPricePerMillion: 18,
		Source:                "user",
	}); err != nil {
		t.Fatalf("UpsertModelConfig() error = %v", err)
	}

	result, err := SyncOpenRouterModelList(context.Background(), []OpenRouterRemoteModel{
		{
			ID:          "openai/gpt-openrouter-test",
			Description: "Remote description",
			Pricing: OpenRouterRemotePricing{
				Prompt:         "0.00000175",
				Completion:     "0.000014",
				InputCacheRead: "0.000000175",
			},
		},
	})
	if err != nil {
		t.Fatalf("SyncOpenRouterModelList() error = %v", err)
	}
	if result.Seen != 1 || result.Added != 0 || result.Updated != 1 || result.Skipped != 0 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	model, ok := GetModelConfig("gpt-openrouter-test")
	if !ok {
		t.Fatal("expected existing model config")
	}
	if model.OwnedBy != "custom-owner" || model.Description != "Local override" || model.Source != "user" {
		t.Fatalf("existing user metadata should not be overwritten: %+v", model)
	}
	if model.InputPricePerMillion != 1.75 || model.OutputPricePerMillion != 14 || model.CachedPricePerMillion != 0.175 {
		t.Fatalf("existing user model pricing should be synced: %+v", model)
	}
}

func TestSyncOpenRouterModelsStripsProviderPrefixAndTildeFromImportedModelID(t *testing.T) {
	initModelConfigTestDB(t)

	result, err := SyncOpenRouterModelList(context.Background(), []OpenRouterRemoteModel{
		{
			ID:          "~moonshotai/kimi-latest",
			Description: "Moonshot latest alias",
			Pricing: OpenRouterRemotePricing{
				Prompt:     "0.0000007448",
				Completion: "0.000004655",
			},
		},
	})
	if err != nil {
		t.Fatalf("SyncOpenRouterModelList() error = %v", err)
	}
	if result.Seen != 1 || result.Added != 1 || result.Updated != 0 || result.Skipped != 0 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	model, ok := GetModelConfig("kimi-latest")
	if !ok {
		t.Fatal("expected OpenRouter alias model to be imported with a local model id")
	}
	if model.ModelID != "kimi-latest" {
		t.Fatalf("model id should strip OpenRouter provider prefix, got %q", model.ModelID)
	}
	if _, ok := GetModelConfig("~moonshotai/kimi-latest"); ok {
		t.Fatal("did not expect OpenRouter alias marker to be stored in model id")
	}
	if model.OwnedBy != "moonshotai" {
		t.Fatalf("owner should not keep OpenRouter alias marker, got %q", model.OwnedBy)
	}
}

func TestSyncOpenRouterModelsNormalizesAnthropicVersionDots(t *testing.T) {
	initModelConfigTestDB(t)

	result, err := SyncOpenRouterModelList(context.Background(), []OpenRouterRemoteModel{
		{
			ID:          "anthropic/claude-sonnet-4.6",
			Description: "Claude Sonnet 4.6",
			Pricing: OpenRouterRemotePricing{
				Prompt:     "0.000003",
				Completion: "0.000015",
			},
		},
	})
	if err != nil {
		t.Fatalf("SyncOpenRouterModelList() error = %v", err)
	}
	if result.Seen != 1 || result.Added != 0 || result.Updated != 1 || result.Skipped != 0 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	model, ok := GetModelConfig("claude-sonnet-4-6")
	if !ok {
		t.Fatal("expected anthropic model to use the local Claude id")
	}
	if model.OwnedBy != "anthropic" || model.InputPricePerMillion != 3 || model.OutputPricePerMillion != 15 {
		t.Fatalf("unexpected normalized anthropic model: %+v", model)
	}
	if _, ok := GetModelConfig("claude-sonnet-4.6"); ok {
		t.Fatal("did not expect dotted Anthropic version id to be stored")
	}
}

func TestSyncOpenRouterModelsMigratesExistingOpenRouterPrefixedRows(t *testing.T) {
	initModelConfigTestDB(t)

	if err := UpsertModelConfig(ModelConfigRow{
		ModelID:               "openai/gpt-openrouter-legacy",
		OwnedBy:               "openai",
		Description:           "Existing prefixed import",
		Enabled:               true,
		PricingMode:           "token",
		InputPricePerMillion:  9,
		OutputPricePerMillion: 18,
		Source:                "openrouter",
	}); err != nil {
		t.Fatalf("UpsertModelConfig() error = %v", err)
	}

	result, err := SyncOpenRouterModelList(context.Background(), []OpenRouterRemoteModel{
		{
			ID:          "openai/gpt-openrouter-legacy",
			Description: "Remote description",
			Pricing: OpenRouterRemotePricing{
				Prompt:     "0.000002",
				Completion: "0.000008",
			},
		},
	})
	if err != nil {
		t.Fatalf("SyncOpenRouterModelList() error = %v", err)
	}
	if result.Seen != 1 || result.Added != 0 || result.Updated != 1 || result.Skipped != 0 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	model, ok := GetModelConfig("gpt-openrouter-legacy")
	if !ok {
		t.Fatal("expected existing OpenRouter row to be migrated to local model id")
	}
	if _, ok := GetModelConfig("openai/gpt-openrouter-legacy"); ok {
		t.Fatal("did not expect old prefixed OpenRouter row to remain")
	}
	if model.Description != "Existing prefixed import" || model.Source != "openrouter" || model.OwnedBy != "openai" {
		t.Fatalf("existing OpenRouter metadata should otherwise stay unchanged: %+v", model)
	}
	if model.InputPricePerMillion != 2 || model.OutputPricePerMillion != 8 {
		t.Fatalf("existing OpenRouter pricing should be synced: %+v", model)
	}
}

func TestOpenRouterPricePerMillionRoundsFloatArtifacts(t *testing.T) {
	if got := openRouterPricePerMillion("0.0000002"); got != 0.2 {
		t.Fatalf("expected clean per-million price, got %.17g", got)
	}
}
