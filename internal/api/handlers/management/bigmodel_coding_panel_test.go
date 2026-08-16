package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPutBigModelCodingKeysCreatesDedicatedEntry(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "qwen",
				BaseURL: "https://qwen.example.com/v1",
				Models:  []config.OpenAICompatibilityModel{{Name: "qwen3-coder", Alias: "qwen3-coder"}},
			},
		},
	}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	rec := runBigModelCodingPanelRequest(t, h, http.MethodPut, `{"api-key":"sk-bigmodel"}`, h.PutBigModelCodingKeys)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("openai compatibility len = %d, want unchanged 1", len(cfg.OpenAICompatibility))
	}
	if len(cfg.BigModelCodingAPIKey) != 1 {
		t.Fatalf("bigmodel coding len = %d, want 1", len(cfg.BigModelCodingAPIKey))
	}
	entry := cfg.BigModelCodingAPIKey[0]
	if entry.Name != bigModelCodingProviderName {
		t.Fatalf("name = %q, want %q", entry.Name, bigModelCodingProviderName)
	}
	if entry.BaseURL != bigModelCodingBaseURL {
		t.Fatalf("base-url = %q, want %q", entry.BaseURL, bigModelCodingBaseURL)
	}
	if entry.IdentityFingerprint != "codex" {
		t.Fatalf("identity fingerprint = %q, want codex", entry.IdentityFingerprint)
	}
	if len(entry.APIKeyEntries) != 1 || entry.APIKeyEntries[0].APIKey != "sk-bigmodel" {
		t.Fatalf("api key entries = %#v", entry.APIKeyEntries)
	}
	assertNoAutoInjectedDefaultAlias(t, entry.Models)
}

func TestPatchBigModelCodingKeyPreservesTargetedDefaults(t *testing.T) {
	cfg := &config.Config{}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	body := `{"value":{"disabled":true,"api-key-entries":[{"api-key":"sk-bigmodel"}],"models":[{"name":"glm-5.1","alias":"custom"}],"identity-fingerprint":"none"}}`
	rec := runBigModelCodingPanelRequest(t, h, http.MethodPatch, body, h.PatchBigModelCodingKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(cfg.BigModelCodingAPIKey) != 1 {
		t.Fatalf("bigmodel coding len = %d, want 1", len(cfg.BigModelCodingAPIKey))
	}
	entry := cfg.BigModelCodingAPIKey[0]
	if !entry.Disabled {
		t.Fatal("expected disabled flag to be preserved")
	}
	if entry.IdentityFingerprint != "codex" {
		t.Fatalf("identity fingerprint = %q, want codex", entry.IdentityFingerprint)
	}
	assertNoAutoInjectedDefaultAlias(t, entry.Models)
}

func TestDeleteBigModelCodingKeyRequiresSelector(t *testing.T) {
	cfg := &config.Config{BigModelCodingAPIKey: []config.OpenAICompatibility{{
		Name:                config.DefaultBigModelCodingProviderName,
		BaseURL:             config.DefaultBigModelCodingBaseURL,
		APIKeyEntries:       []config.OpenAICompatibilityAPIKey{{APIKey: "sk-bigmodel"}},
		IdentityFingerprint: "codex",
	}}}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	missingSelectorRec := runBigModelCodingPanelRequest(t, h, http.MethodDelete, "", h.DeleteBigModelCodingKey)
	if missingSelectorRec.Code != http.StatusBadRequest {
		t.Fatalf("missing selector status = %d, want %d body=%s", missingSelectorRec.Code, http.StatusBadRequest, missingSelectorRec.Body.String())
	}
	if len(cfg.BigModelCodingAPIKey) != 1 {
		t.Fatalf("missing selector changed config: %#v", cfg.BigModelCodingAPIKey)
	}

	deleteRec := runBigModelCodingPanelRequestAtPath(t, h, http.MethodDelete, "/v0/management/bigmodel-coding-api-key?index=0", "", h.DeleteBigModelCodingKey)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d body=%s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}
	if len(cfg.BigModelCodingAPIKey) != 0 {
		t.Fatalf("bigmodel coding keys = %#v, want empty", cfg.BigModelCodingAPIKey)
	}
}

func TestGetBigModelCodingKeysFiltersOpenAICompatEntries(t *testing.T) {
	cfg := &config.Config{
		BigModelCodingAPIKey: []config.OpenAICompatibility{
			{Name: "bigmodel-coding", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4", IdentityFingerprint: "codex"},
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "qwen", BaseURL: "https://qwen.example.com/v1"},
		},
	}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	rec := runBigModelCodingPanelRequest(t, h, http.MethodGet, "", h.GetBigModelCodingKeys)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload map[string][]openAICompatibilityWithAuthIndex
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items := payload["bigmodel-coding"]
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].Name != "bigmodel-coding" || items[0].IdentityFingerprint != "codex" {
		t.Fatalf("unexpected item: %+v", items[0])
	}
}

func assertNoAutoInjectedDefaultAlias(t *testing.T, models []config.OpenAICompatibilityModel) {
	t.Helper()
	for _, model := range models {
		if model.Name == bigModelCodingModel && model.Alias == bigModelCodingAlias {
			t.Fatalf("auto-injected default alias %s -> %s should not be present in %#v", bigModelCodingModel, bigModelCodingAlias, models)
		}
	}
}

func newBigModelCodingPanelTestHandler(t *testing.T, cfg *config.Config) *Handler {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return &Handler{
		cfg:            cfg,
		configFilePath: configPath,
		failedAttempts: make(map[string]*attemptInfo),
	}
}

func runBigModelCodingPanelRequest(t *testing.T, h *Handler, method, body string, fn func(*gin.Context)) *httptest.ResponseRecorder {
	return runBigModelCodingPanelRequestAtPath(t, h, method, "/v0/management/bigmodel-coding-api-key", body, fn)
}

func runBigModelCodingPanelRequestAtPath(t *testing.T, h *Handler, method, path, body string, fn func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	fn(ctx)
	return rec
}
