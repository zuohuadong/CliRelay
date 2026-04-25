package usage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func initModelConfigTestDB(t *testing.T) {
	t.Helper()
	CloseDB()
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	if err := InitDB(dbPath, config.RequestLogStorageConfig{}, time.UTC); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(CloseDB)
}

func TestInitDBSeedsDefaultModelConfigs(t *testing.T) {
	initModelConfigTestDB(t)

	models := ListModelConfigs()
	if len(models) == 0 {
		t.Fatal("expected seeded model configs")
	}

	imageModel, ok := GetModelConfig("gpt-image-2")
	if !ok {
		t.Fatal("expected gpt-image-2 to be seeded")
	}
	if imageModel.PricingMode != "call" {
		t.Fatalf("expected gpt-image-2 pricing mode call, got %q", imageModel.PricingMode)
	}
	if imageModel.PricePerCall <= 0 {
		t.Fatalf("expected gpt-image-2 default per-call price, got %v", imageModel.PricePerCall)
	}

	owners := ListModelOwnerPresets()
	if len(owners) == 0 {
		t.Fatal("expected seeded owner presets")
	}
	if _, ok := GetModelOwnerPreset("openai"); !ok {
		t.Fatal("expected openai owner preset")
	}
}

func TestUpsertModelConfigAndPerCallCost(t *testing.T) {
	initModelConfigTestDB(t)

	err := UpsertModelConfig(ModelConfigRow{
		ModelID:      "custom-image",
		OwnedBy:      "acme-ai",
		Description:  "Custom image model",
		Enabled:      true,
		PricingMode:  "call",
		PricePerCall: 0.12,
	})
	if err != nil {
		t.Fatalf("UpsertModelConfig() error = %v", err)
	}

	model, ok := GetModelConfig("custom-image")
	if !ok {
		t.Fatal("expected custom model config")
	}
	if model.OwnedBy != "acme-ai" || model.PricePerCall != 0.12 {
		t.Fatalf("unexpected model config: %+v", model)
	}

	cost := CalculateCost("custom-image", 123, 456, 0)
	if cost != 0.12 {
		t.Fatalf("expected per-call cost 0.12, got %v", cost)
	}
}

func TestDeleteModelConfigRemovesConfigAndPricing(t *testing.T) {
	initModelConfigTestDB(t)

	if err := UpsertModelConfig(ModelConfigRow{
		ModelID:               "temporary-model",
		OwnedBy:               "openai",
		Enabled:               true,
		PricingMode:           "token",
		InputPricePerMillion:  1,
		OutputPricePerMillion: 2,
	}); err != nil {
		t.Fatalf("UpsertModelConfig() error = %v", err)
	}

	if err := DeleteModelConfig("temporary-model"); err != nil {
		t.Fatalf("DeleteModelConfig() error = %v", err)
	}
	if _, ok := GetModelConfig("temporary-model"); ok {
		t.Fatal("expected model config to be deleted")
	}
	if cost := CalculateCost("temporary-model", 1_000_000, 1_000_000, 0); cost != 0 {
		t.Fatalf("expected deleted model cost 0, got %v", cost)
	}
}
