package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestValidateConfig_ClampsUnsupportedLevelAcrossOpenAIFormats(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID: "glm-5.1",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}
	config := ThinkingConfig{
		Mode:  ModeLevel,
		Level: LevelXHigh,
	}

	got, err := ValidateConfig(config, modelInfo, "codex", "openai", false)
	if err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	if got == nil {
		t.Fatal("ValidateConfig() returned nil config")
	}
	if got.Mode != ModeLevel {
		t.Fatalf("mode = %v, want %v", got.Mode, ModeLevel)
	}
	if got.Level != LevelHigh {
		t.Fatalf("level = %q, want %q", got.Level, LevelHigh)
	}
}

func TestValidateConfig_RejectsUnsupportedLevelWithinSameFormat(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID: "glm-5.1",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}
	config := ThinkingConfig{
		Mode:  ModeLevel,
		Level: LevelXHigh,
	}

	got, err := ValidateConfig(config, modelInfo, "openai", "openai", false)
	if err == nil {
		t.Fatal("expected unsupported level error")
	}
	if got != nil {
		t.Fatalf("expected nil config on error, got %#v", got)
	}
}
