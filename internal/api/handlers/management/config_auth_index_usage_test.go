package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestConfigListEndpointsIncludeUsageStats(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	idGen := synthesizer.NewStableIDGenerator()

	openAICompatID, _ := idGen.Next("openai-compatibility:bohe", "compat-key", "https://compat.example.com", "")
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       openAICompatID,
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"api_key":  "compat-key",
			"base_url": "https://compat.example.com",
		},
	}); err != nil {
		t.Fatalf("register openai-compat auth: %v", err)
	}

	codexID, _ := idGen.Next("codex:apikey", "codex-key", "https://codex.example.com")
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       codexID,
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "codex-key",
			"base_url": "https://codex.example.com",
		},
	}); err != nil {
		t.Fatalf("register codex auth: %v", err)
	}

	manager.MarkResult(context.Background(), coreauth.Result{AuthID: openAICompatID, Provider: "openai-compatibility", Model: "gpt-4.1", Success: true})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: openAICompatID, Provider: "openai-compatibility", Model: "gpt-4.1", Success: true})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: openAICompatID, Provider: "openai-compatibility", Model: "gpt-4.1", Success: false})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: codexID, Provider: "codex", Model: "gpt-5.3-codex", Success: true})

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "bohe",
				BaseURL: "https://compat.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "compat-key"},
				},
			},
		},
		CodexKey: []config.CodexKey{
			{
				APIKey:  "codex-key",
				BaseURL: "https://codex.example.com",
			},
		},
	}, manager)

	t.Run("openai compatibility", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)

		h.GetOpenAICompat(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var payload map[string][]openAICompatibilityWithAuthIndex
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}

		items := payload["openai-compatibility"]
		if len(items) != 1 {
			t.Fatalf("items len = %d, want 1", len(items))
		}
		entry := items[0]
		if entry.Success != 2 || entry.Failed != 1 {
			t.Fatalf("provider totals = %d/%d, want 2/1", entry.Success, entry.Failed)
		}
		if len(entry.RecentRequests) != 20 {
			t.Fatalf("provider recent bucket len = %d, want 20", len(entry.RecentRequests))
		}
		if len(entry.APIKeyEntries) != 1 {
			t.Fatalf("api key entries len = %d, want 1", len(entry.APIKeyEntries))
		}
		if entry.APIKeyEntries[0].Success != 2 || entry.APIKeyEntries[0].Failed != 1 {
			t.Fatalf("api key totals = %d/%d, want 2/1", entry.APIKeyEntries[0].Success, entry.APIKeyEntries[0].Failed)
		}
		if len(entry.APIKeyEntries[0].RecentRequests) != 20 {
			t.Fatalf("api key recent bucket len = %d, want 20", len(entry.APIKeyEntries[0].RecentRequests))
		}
	})

	t.Run("codex api key", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-api-key", nil)

		h.GetCodexKeys(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var payload map[string][]codexKeyWithAuthIndex
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}

		items := payload["codex-api-key"]
		if len(items) != 1 {
			t.Fatalf("items len = %d, want 1", len(items))
		}
		entry := items[0]
		if entry.Success != 1 || entry.Failed != 0 {
			t.Fatalf("totals = %d/%d, want 1/0", entry.Success, entry.Failed)
		}
		if len(entry.RecentRequests) != 20 {
			t.Fatalf("recent bucket len = %d, want 20", len(entry.RecentRequests))
		}
	})
}
