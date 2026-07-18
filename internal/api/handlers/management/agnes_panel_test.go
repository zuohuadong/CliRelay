package management

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetAgnesKeysReturnsDedicatedConfig(t *testing.T) {
	cfg := &config.Config{
		AgnesAPIKey: []config.OpenAICompatibility{
			{
				Name:    config.DefaultAgnesProviderName,
				BaseURL: config.DefaultAgnesBaseURL,
				Models: []config.OpenAICompatibilityModel{
					{Name: config.DefaultAgnesChatModel},
					{Name: config.DefaultAgnesVideoModel, Video: true},
				},
			},
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "qwen", BaseURL: "https://qwen.example.com/v1"},
		},
	}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	rec := runBigModelCodingPanelRequest(t, h, http.MethodGet, "", h.GetAgnesKeys)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload map[string][]openAICompatibilityWithAuthIndex
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items := payload["agnes-api-key"]
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].Name != config.DefaultAgnesProviderName {
		t.Fatalf("name = %q, want %q", items[0].Name, config.DefaultAgnesProviderName)
	}
	if len(items[0].Models) != 2 || !items[0].Models[1].Video {
		t.Fatalf("unexpected models: %#v", items[0].Models)
	}
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].Name != "qwen" {
		t.Fatalf("generic openai compatibility changed: %#v", cfg.OpenAICompatibility)
	}
}

func TestPutAgnesKeysPersistsDedicatedConfig(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "qwen", BaseURL: "https://qwen.example.com/v1"},
		},
	}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	body := `[{"name":"agnes-ai","api-key-entries":[{"api-key":"sk-agnes"}],"models":[{"name":"agnes-2.0-flash"},{"name":"agnes-image-2.1-flash"},{"name":"agnes-video-v2.0"}]}]`
	rec := runBigModelCodingPanelRequest(t, h, http.MethodPut, body, h.PutAgnesKeys)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].Name != "qwen" {
		t.Fatalf("generic openai compatibility changed: %#v", cfg.OpenAICompatibility)
	}
	if len(cfg.AgnesAPIKey) != 1 {
		t.Fatalf("agnes len = %d, want 1", len(cfg.AgnesAPIKey))
	}
	entry := cfg.AgnesAPIKey[0]
	if entry.Name != config.DefaultAgnesProviderName || entry.BaseURL != config.DefaultAgnesBaseURL {
		t.Fatalf("unexpected Agnes defaults: %#v", entry)
	}
	if entry.TestModel != config.DefaultAgnesChatModel {
		t.Fatalf("test model = %q, want %q", entry.TestModel, config.DefaultAgnesChatModel)
	}
	if len(entry.Models) != 3 || !entry.Models[1].Image || !entry.Models[2].Video {
		t.Fatalf("unexpected models: %#v", entry.Models)
	}
}

func TestPatchAndDeleteAgnesKey(t *testing.T) {
	cfg := &config.Config{}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	patchBody := `{"value":{"api-key-entries":[{"api-key":"sk-agnes"}],"models":[{"name":"agnes-video-v2.0"}]}}`
	patchRec := runBigModelCodingPanelRequest(t, h, http.MethodPatch, patchBody, h.PatchAgnesKey)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want %d body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	if len(cfg.AgnesAPIKey) != 1 || len(cfg.AgnesAPIKey[0].Models) != 1 || !cfg.AgnesAPIKey[0].Models[0].Video {
		t.Fatalf("unexpected Agnes config after patch: %#v", cfg.AgnesAPIKey)
	}

	deleteRec := runBigModelCodingPanelRequest(t, h, http.MethodDelete, "", h.DeleteAgnesKey)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d body=%s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}
	if len(cfg.AgnesAPIKey) != 0 {
		t.Fatalf("agnes len = %d, want 0", len(cfg.AgnesAPIKey))
	}
}

func TestPatchAgnesKeyTargetsRequestedIndex(t *testing.T) {
	cfg := &config.Config{
		AgnesAPIKey: []config.OpenAICompatibility{
			{Name: config.DefaultAgnesProviderName, BaseURL: "https://first.example/v1"},
			{Name: config.DefaultAgnesProviderName, BaseURL: "https://second.example/v1"},
		},
	}
	h := newBigModelCodingPanelTestHandler(t, cfg)
	body := `{"index":1,"value":{"disabled":true}}`

	rec := runBigModelCodingPanelRequest(t, h, http.MethodPatch, body, h.PatchAgnesKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if cfg.AgnesAPIKey[0].Disabled || !cfg.AgnesAPIKey[1].Disabled {
		t.Fatalf("unexpected targeted patch result: %#v", cfg.AgnesAPIKey)
	}
}

func TestPutAgnesKeysPreservesMaskedAPIKeyAndDedicatedYAML(t *testing.T) {
	fullKey := "sk-agnes-secret-1234"
	cfg := &config.Config{
		AgnesAPIKey: []config.OpenAICompatibility{
			{
				Name:          config.DefaultAgnesProviderName,
				BaseURL:       config.DefaultAgnesBaseURL,
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: fullKey}},
			},
		},
	}
	h := newBigModelCodingPanelTestHandler(t, cfg)
	body := `[{"name":"agnes","base-url":"https://apihub.agnes-ai.com/v1","api-key-entries":[{"api-key":"` + maskAPIKey(fullKey) + `"}]}]`

	rec := runBigModelCodingPanelRequest(t, h, http.MethodPut, body, h.PutAgnesKeys)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := cfg.AgnesAPIKey[0].APIKeyEntries[0].APIKey; got != fullKey {
		t.Fatalf("api key = %q, want restored full key", got)
	}
	persisted, err := os.ReadFile(h.configFilePath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	text := string(persisted)
	if !strings.Contains(text, "agnes:\n") || strings.Contains(text, "sk-***") {
		t.Fatalf("unexpected persisted config:\n%s", text)
	}
}

func TestPutOpenAICompatRoutesAgnesToDedicatedConfig(t *testing.T) {
	cfg := &config.Config{}
	h := newBigModelCodingPanelTestHandler(t, cfg)
	body := `[{"name":"agnes-ai","base-url":"https://apihub.agnes-ai.com/v1","api-key-entries":[{"api-key":"sk-agnes"}]},{"name":"qwen","base-url":"https://qwen.example.com/v1"}]`

	rec := runBigModelCodingPanelRequest(t, h, http.MethodPut, body, h.PutOpenAICompat)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(cfg.AgnesAPIKey) != 1 || cfg.AgnesAPIKey[0].Name != config.DefaultAgnesProviderName {
		t.Fatalf("unexpected dedicated Agnes config: %#v", cfg.AgnesAPIKey)
	}
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].Name != "qwen" {
		t.Fatalf("unexpected generic config: %#v", cfg.OpenAICompatibility)
	}
}

func TestPatchOpenAICompatRoutesAgnesWithOriginalBody(t *testing.T) {
	cfg := &config.Config{
		AgnesAPIKey: []config.OpenAICompatibility{
			{Name: config.DefaultAgnesProviderName, BaseURL: config.DefaultAgnesBaseURL},
		},
	}
	h := newBigModelCodingPanelTestHandler(t, cfg)
	body := `{"name":"agnes","value":{"disabled":true}}`

	rec := runBigModelCodingPanelRequest(t, h, http.MethodPatch, body, h.PatchOpenAICompat)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(cfg.AgnesAPIKey) != 1 || !cfg.AgnesAPIKey[0].Disabled {
		t.Fatalf("unexpected dedicated Agnes config: %#v", cfg.AgnesAPIKey)
	}
}
