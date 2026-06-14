package config

import (
	"os"
	"strings"
	"testing"
)

func TestParseConfigBytesMigratesLegacyBigModelCodingEntry(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
openai-compatibility:
  - name: bigmodel-coding
    base-url: https://open.bigmodel.cn/api/coding/paas/v4
    api-key-entries:
      - api-key: sk-bigmodel
    models:
      - name: glm-5.1
        alias: gpt-5.3-codex
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    models:
      - name: openai/gpt-oss
        alias: gpt-oss
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.BigModelCodingAPIKey) != 1 {
		t.Fatalf("bigmodel-coding len = %d, want 1", len(cfg.BigModelCodingAPIKey))
	}
	entry := cfg.BigModelCodingAPIKey[0]
	if entry.Name != DefaultBigModelCodingProviderName {
		t.Fatalf("name = %q, want %q", entry.Name, DefaultBigModelCodingProviderName)
	}
	if entry.IdentityFingerprint != "codex" {
		t.Fatalf("identity-fingerprint = %q, want codex", entry.IdentityFingerprint)
	}
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].Name != "openrouter" {
		t.Fatalf("openai-compatibility = %#v", cfg.OpenAICompatibility)
	}
}

func TestParseConfigBytesReadsDedicatedBigModelCodingKey(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
bigmodel-coding:
  - base-url: https://open.bigmodel.cn/api/coding/paas/v4
    api-key-entries:
      - api-key: sk-bigmodel
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.BigModelCodingAPIKey) != 1 {
		t.Fatalf("bigmodel-coding len = %d, want 1", len(cfg.BigModelCodingAPIKey))
	}
	if len(cfg.BigModelCodingAPIKeyLegacy) != 0 {
		t.Fatalf("legacy bigmodel-coding-api-key len = %d, want 0", len(cfg.BigModelCodingAPIKeyLegacy))
	}
	entry := cfg.BigModelCodingAPIKey[0]
	if entry.Name != DefaultBigModelCodingProviderName {
		t.Fatalf("name = %q, want %q", entry.Name, DefaultBigModelCodingProviderName)
	}
	if entry.BaseURL != DefaultBigModelCodingBaseURL {
		t.Fatalf("base-url = %q, want %q", entry.BaseURL, DefaultBigModelCodingBaseURL)
	}
}

func TestParseConfigBytesReadsLegacyBigModelCodingAPIKey(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
bigmodel-coding-api-key:
  - base-url: https://open.bigmodel.cn/api/coding/paas/v4
    api-key-entries:
      - api-key: sk-bigmodel
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.BigModelCodingAPIKey) != 1 {
		t.Fatalf("bigmodel-coding len = %d, want 1", len(cfg.BigModelCodingAPIKey))
	}
	if len(cfg.BigModelCodingAPIKeyLegacy) != 0 {
		t.Fatalf("legacy bigmodel-coding-api-key len = %d, want 0", len(cfg.BigModelCodingAPIKeyLegacy))
	}
}

func TestSaveConfigPreserveCommentsWritesDedicatedBigModelCodingKey(t *testing.T) {
	data := []byte(`
bigmodel-coding-api-key:
  - base-url: https://open.bigmodel.cn/api/coding/paas/v4
    api-key-entries:
      - api-key: sk-bigmodel
`)
	cfg, err := ParseConfigBytes(data)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	out := string(written)
	if !strings.Contains(out, "\nbigmodel-coding:") {
		t.Fatalf("saved config missing bigmodel-coding key:\n%s", out)
	}
	if strings.Contains(out, "bigmodel-coding-api-key:") {
		t.Fatalf("saved config still contains legacy key:\n%s", out)
	}
}

func TestSanitizeBigModelCodingAddsDefaults(t *testing.T) {
	cfg := &Config{BigModelCodingAPIKey: []OpenAICompatibility{{APIKeyEntries: []OpenAICompatibilityAPIKey{{APIKey: "sk"}}}}}
	cfg.SanitizeBigModelCoding()
	if len(cfg.BigModelCodingAPIKey) != 1 {
		t.Fatalf("bigmodel-coding len = %d, want 1", len(cfg.BigModelCodingAPIKey))
	}
	entry := cfg.BigModelCodingAPIKey[0]
	if entry.Name != DefaultBigModelCodingProviderName {
		t.Fatalf("name = %q", entry.Name)
	}
	if entry.BaseURL != DefaultBigModelCodingBaseURL {
		t.Fatalf("base-url = %q", entry.BaseURL)
	}
	if entry.TestModel != DefaultBigModelCodingModel {
		t.Fatalf("test-model = %q", entry.TestModel)
	}
	// Default alias is NOT auto-injected; user must configure models explicitly.
	if len(entry.Models) != 0 {
		t.Fatalf("expected no auto-injected models, got %#v", entry.Models)
	}
}

func TestSanitizeAstronCodeAddsGLM51Alias(t *testing.T) {
	cfg := &Config{AstronCodeAPIKey: []OpenAICompatibility{{APIKeyEntries: []OpenAICompatibilityAPIKey{{APIKey: "sk"}}}}}
	cfg.SanitizeAstronCode()
	if len(cfg.AstronCodeAPIKey) != 1 {
		t.Fatalf("astron-code len = %d, want 1", len(cfg.AstronCodeAPIKey))
	}
	entry := cfg.AstronCodeAPIKey[0]
	if entry.Name != DefaultAstronCodeProviderName {
		t.Fatalf("name = %q", entry.Name)
	}
	if entry.BaseURL != DefaultAstronCodeBaseURL {
		t.Fatalf("base-url = %q", entry.BaseURL)
	}
	if entry.TestModel != DefaultAstronCodeModel {
		t.Fatalf("test-model = %q", entry.TestModel)
	}
	// Default aliases are NOT auto-injected; user must configure models explicitly.
	if len(entry.Models) != 0 {
		t.Fatalf("expected no auto-injected models, got %#v", entry.Models)
	}
}

func hasModelAlias(models []OpenAICompatibilityModel, name, alias string) bool {
	for _, model := range models {
		if model.Name == name && model.Alias == alias {
			return true
		}
	}
	return false
}
