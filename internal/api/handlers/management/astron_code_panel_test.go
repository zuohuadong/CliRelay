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
