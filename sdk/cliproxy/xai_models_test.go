package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestFetchXAIModelsForAuthCachesPerAuthIdentity(t *testing.T) {
	resetXAIModelCacheForTest()
	t.Cleanup(resetXAIModelCacheForTest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-a" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-dynamic","aliases":["grok-dynamic-alias"],"context_length":200000,"max_completion_tokens":4096}]}`))
	}))
	defer server.Close()

	service := &Service{cfg: &sdkconfig.Config{}}
	authA := &coreauth.Auth{
		ID:       "account-a",
		Provider: "xai",
		Metadata: map[string]any{"access_token": "token-a", "auth_kind": "oauth", "base_url": server.URL},
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  server.URL,
		},
	}

	models := service.fetchXAIModelsForAuth(context.Background(), authA)
	if !hasModelID(models, "grok-dynamic") || !hasModelID(models, "grok-dynamic-alias") {
		t.Fatalf("fetched models = %#v, want model and alias", models)
	}
	aliases := coreauth.OAuthModelAliasesFromAttributes(authA.Attributes)
	if len(aliases) != 1 || aliases[0].Name != "grok-dynamic" || aliases[0].Alias != "grok-dynamic-alias" {
		t.Fatalf("stored aliases = %#v, want dynamic alias mapping", aliases)
	}

	server.Close()
	cached := service.fetchXAIModelsForAuth(context.Background(), authA)
	if !hasModelID(cached, "grok-dynamic") {
		t.Fatalf("cached models = %#v, want account-a dynamic model", cached)
	}

	authB := &coreauth.Auth{
		ID:       "account-b",
		Provider: "xai",
		Metadata: map[string]any{"access_token": "token-b", "auth_kind": "oauth", "base_url": server.URL},
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  server.URL,
		},
	}
	isolated := service.fetchXAIModelsForAuth(context.Background(), authB)
	if hasModelID(isolated, "grok-dynamic") {
		t.Fatalf("account-b models leaked account-a cache: %#v", isolated)
	}
}

func hasModelID(models []*ModelInfo, want string) bool {
	for _, model := range models {
		if model != nil && model.ID == want {
			return true
		}
	}
	return false
}

func resetXAIModelCacheForTest() {
	xaiModelCache.mu.Lock()
	xaiModelCache.byAuth = make(map[string][]*ModelInfo)
	xaiModelCache.mu.Unlock()
}
