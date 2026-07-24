package management

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetAstronCodeKeysReturnsBillingMultiplier(t *testing.T) {
	cfg := &config.Config{
		AstronCodeAPIKey: []config.OpenAICompatibility{{
			Name:              "astron-code",
			BaseURL:           config.DefaultAstronCodeBaseURL,
			BillingMultiplier: 0.1,
			APIKeyEntries:     []config.OpenAICompatibilityAPIKey{{APIKey: "sk-astron"}},
		}},
	}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	rec := runBigModelCodingPanelRequest(t, h, http.MethodGet, "", h.GetAstronCodeKeys)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string][]openAICompatibilityWithAuthIndex
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items := payload["astron-code"]
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if got := items[0].BillingMultiplier; got != 0.1 {
		t.Fatalf("billing multiplier = %v, want 0.1", got)
	}
}

func TestPutAstronCodeKeysDefaultsResponseEndpoint(t *testing.T) {
	cfg := &config.Config{}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	body := `[{"name":"astron-code","base-url":"https://maas-coding-api.cn-huabei-1.xf-yun.com/v1","api-key-entries":[{"api-key":"sk-astron"}],"models":[{"name":"xopkimik26","alias":"kimi-k2.6"}]}]`
	rec := runBigModelCodingPanelRequest(t, h, http.MethodPut, body, h.PutAstronCodeKeys)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(cfg.AstronCodeAPIKey) != 1 {
		t.Fatalf("astron-code len = %d, want 1", len(cfg.AstronCodeAPIKey))
	}
	entry := cfg.AstronCodeAPIKey[0]
	if !entry.ResponseEndpoint {
		t.Fatal("expected response endpoint to default on")
	}
	if entry.IdentityFingerprint != "codex" {
		t.Fatalf("identity fingerprint = %q, want codex", entry.IdentityFingerprint)
	}
}

func TestPatchAstronCodeKeyRestoresResponseEndpoint(t *testing.T) {
	cfg := &config.Config{
		AstronCodeAPIKey: []config.OpenAICompatibility{{
			Name: "astron-code",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey: "sk-astron",
			}},
			Models: []config.OpenAICompatibilityModel{{Name: "xopkimik26", Alias: "kimi-k2.6"}},
		}},
	}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	rec := runBigModelCodingPanelRequest(t, h, http.MethodPatch, `{"value":{"response-endpoint":false}}`, h.PatchAstronCodeKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !cfg.AstronCodeAPIKey[0].ResponseEndpoint {
		t.Fatal("expected response endpoint to be restored")
	}
}

func TestPatchAstronCodeKeyForceChatCompletionsDisablesResponseEndpoint(t *testing.T) {
	cfg := &config.Config{
		AstronCodeAPIKey: []config.OpenAICompatibility{{
			Name: astronCodeProviderName,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey: "sk-astron",
			}},
			Models: []config.OpenAICompatibilityModel{{Name: "xopglm52", Alias: "glm-5.2"}},
		}},
	}
	h := newBigModelCodingPanelTestHandler(t, cfg)

	rec := runBigModelCodingPanelRequest(t, h, http.MethodPatch, `{"value":{"force-chat-completions":true}}`, h.PatchAstronCodeKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := cfg.AstronCodeAPIKey[0]
	if !entry.ForceChatCompletions {
		t.Fatal("expected force-chat-completions to be enabled")
	}
	if entry.ResponseEndpoint {
		t.Fatal("force-chat-completions should disable the response endpoint")
	}
}
