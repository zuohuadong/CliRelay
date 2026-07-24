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

func TestSanitizeAstronCodeDoesNotInjectAliasesForChatEndpoint(t *testing.T) {
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
	if !entry.ResponseEndpoint {
		t.Fatal("expected astron-code response endpoint to default on")
	}
	// Default aliases are NOT auto-injected; user must configure models explicitly.
	if len(entry.Models) != 0 {
		t.Fatalf("expected no auto-injected models, got %#v", entry.Models)
	}
}

func TestLoadConfigOptionalAstronForceChatCompletionsDisablesResponsesEndpoint(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte(`
astron-code:
  - base-url: https://maas-coding-api.cn-huabei-1.xf-yun.com/v1
    force-chat-completions: true
    response-endpoint: false
    api-key-entries:
      - api-key: sk-astron
    models:
      - name: xopglm52
        alias: glm-5.2
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if len(cfg.AstronCodeAPIKey) != 1 {
		t.Fatalf("astron-code len = %d, want 1", len(cfg.AstronCodeAPIKey))
	}
	if cfg.AstronCodeAPIKey[0].ResponseEndpoint {
		t.Fatal("force-chat-completions should keep the Responses endpoint disabled")
	}
}

func TestSanitizeAstronCodeRestoresResponseEndpoint(t *testing.T) {
	cfg := &Config{AstronCodeAPIKey: []OpenAICompatibility{{
		ResponseEndpoint: false,
		APIKeyEntries:    []OpenAICompatibilityAPIKey{{APIKey: "sk"}},
		Models: []OpenAICompatibilityModel{
			{Name: "xopkimik26", Alias: "kimi-k2.6"},
		},
	}}}
	cfg.SanitizeAstronCode()
	if len(cfg.AstronCodeAPIKey) != 1 {
		t.Fatalf("astron-code len = %d, want 1", len(cfg.AstronCodeAPIKey))
	}
	if !cfg.AstronCodeAPIKey[0].ResponseEndpoint {
		t.Fatal("expected astron-code response endpoint to be restored")
	}
	if !hasModelAlias(cfg.AstronCodeAPIKey[0].Models, "xopkimik26", "kimi-k2.6") {
		t.Fatalf("missing explicit kimi alias in %#v", cfg.AstronCodeAPIKey[0].Models)
	}
}

func TestSanitizeAstronCodeResponseEndpointDoesNotInjectAliases(t *testing.T) {
	cfg := &Config{AstronCodeAPIKey: []OpenAICompatibility{{
		ResponseEndpoint: true,
		APIKeyEntries:    []OpenAICompatibilityAPIKey{{APIKey: "sk"}},
	}}}
	cfg.SanitizeAstronCode()
	if len(cfg.AstronCodeAPIKey) != 1 {
		t.Fatalf("astron-code len = %d, want 1", len(cfg.AstronCodeAPIKey))
	}
	models := cfg.AstronCodeAPIKey[0].Models
	if len(models) != 0 {
		t.Fatalf("expected no auto-injected models, got %#v", models)
	}
}

func TestSanitizeAstronCodeResponseEndpointKeepsExplicitAliasOnly(t *testing.T) {
	cfg := &Config{AstronCodeAPIKey: []OpenAICompatibility{{
		ResponseEndpoint: true,
		APIKeyEntries:    []OpenAICompatibilityAPIKey{{APIKey: "sk"}},
		Models: []OpenAICompatibilityModel{
			{Name: "xopdeepseekv4pro", Alias: "deepseek-v4-pro", ContextLength: 1000000},
		},
	}}}
	cfg.SanitizeAstronCode()
	if len(cfg.AstronCodeAPIKey) != 1 {
		t.Fatalf("astron-code len = %d, want 1", len(cfg.AstronCodeAPIKey))
	}
	models := cfg.AstronCodeAPIKey[0].Models
	if !hasModelAlias(models, "xopdeepseekv4pro", "deepseek-v4-pro") {
		t.Fatalf("missing explicit deepseek alias in %#v", models)
	}
	if hasModelAlias(models, DefaultAstronCodeModel, "deepseek-v4-pro") {
		t.Fatalf("unexpected default deepseek alias in %#v", models)
	}
	if hasModelAlias(models, DefaultAstronCodeModel, "gpt-5.3-codex") {
		t.Fatalf("unexpected default codex alias in %#v", models)
	}
}

func TestSanitizeAstronCodeUsesExplicitModelAsDefaultTestModel(t *testing.T) {
	cfg := &Config{AstronCodeAPIKey: []OpenAICompatibility{{
		ResponseEndpoint: true,
		APIKeyEntries:    []OpenAICompatibilityAPIKey{{APIKey: "sk"}},
		Models: []OpenAICompatibilityModel{
			{Name: "xopdeepseekv4pro", Alias: "deepseek-v4-pro", ContextLength: 1000000},
		},
	}}}
	cfg.SanitizeAstronCode()
	if len(cfg.AstronCodeAPIKey) != 1 {
		t.Fatalf("astron-code len = %d, want 1", len(cfg.AstronCodeAPIKey))
	}
	if got := cfg.AstronCodeAPIKey[0].TestModel; got != "xopdeepseekv4pro" {
		t.Fatalf("test-model = %q, want xopdeepseekv4pro", got)
	}
	if hasModelAlias(cfg.AstronCodeAPIKey[0].Models, DefaultAstronCodeModel, "deepseek-v4-pro") {
		t.Fatalf("unexpected default model alias in %#v", cfg.AstronCodeAPIKey[0].Models)
	}
}

func TestParseConfigBytesMigratesLegacyAgnesEntry(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
openai-compatibility:
  - name: agnes-ai
    base-url: https://apihub.agnes-ai.com/v1/
    api-key-entries:
      - api-key: sk-agnes
    models:
      - name: agnes-2.0-flash
        alias: agnes-2.0-flash
      - name: agnes-image-2.1-flash
        alias: agnes-image-2.1-flash
      - name: agnes-video-v2.0
        alias: agnes-video-v2.0
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    models:
      - name: openai/gpt-oss
        alias: gpt-oss
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.AgnesAPIKey) != 1 {
		t.Fatalf("agnes len = %d, want 1", len(cfg.AgnesAPIKey))
	}
	entry := cfg.AgnesAPIKey[0]
	if entry.Name != DefaultAgnesProviderName {
		t.Fatalf("name = %q, want %q", entry.Name, DefaultAgnesProviderName)
	}
	if entry.BaseURL != "https://apihub.agnes-ai.com/v1/" {
		t.Fatalf("base-url = %q", entry.BaseURL)
	}
	if entry.TestModel != DefaultAgnesChatModel {
		t.Fatalf("test-model = %q, want %q", entry.TestModel, DefaultAgnesChatModel)
	}
	if !entry.Models[1].Image {
		t.Fatalf("image model was not marked image: %#v", entry.Models[1])
	}
	if !entry.Models[2].Video {
		t.Fatalf("video model was not marked video: %#v", entry.Models[2])
	}
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].Name != "openrouter" {
		t.Fatalf("openai-compatibility = %#v", cfg.OpenAICompatibility)
	}
}

func TestOpenAICompatibilityAliasProvidersIsConfigDriven(t *testing.T) {
	cfg := &Config{
		BigModelCodingAPIKey: []OpenAICompatibility{{
			Models: []OpenAICompatibilityModel{{Name: "glm-5.2", Alias: "gpt-5.3-codex"}},
		}},
		AstronCodeAPIKey: []OpenAICompatibility{{
			Models: []OpenAICompatibilityModel{{Name: "astron-code-latest", Alias: "gpt-5.3-codex"}},
		}},
		AgnesAPIKey: []OpenAICompatibility{{
			Models: []OpenAICompatibilityModel{{Name: "agnes-2.0-flash", Alias: "gpt-5.3-codex"}},
		}},
		OpenAICompatibility: []OpenAICompatibility{
			{Name: "custom-coding", Models: []OpenAICompatibilityModel{{Name: "custom-upstream", Alias: "gpt-5.3-codex"}}},
			{Name: "disabled-coding", Disabled: true, Models: []OpenAICompatibilityModel{{Name: "disabled-upstream", Alias: "gpt-5.3-codex"}}},
			{Name: "other-coding", Models: []OpenAICompatibilityModel{{Name: "other-upstream", Alias: "other-model"}}},
		},
	}

	providers := cfg.OpenAICompatibilityAliasProviders("gpt-5.3-codex")
	want := []string{DefaultBigModelCodingProviderName, DefaultAstronCodeProviderName, DefaultAgnesProviderName, "openai-compatible-custom-coding"}
	if strings.Join(providers, ",") != strings.Join(want, ",") {
		t.Fatalf("providers = %v, want %v", providers, want)
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
