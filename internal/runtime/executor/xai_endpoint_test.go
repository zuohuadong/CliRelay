package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// The HTTP Responses endpoint is the ownership seam for the endpoint mode.
// Media, compact, and model discovery deliberately use their own API paths.
func TestXAIResponsesEndpointMode(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "oauth without persisted mode uses Grok Build",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  xaiauth.DefaultAPIBaseURL,
			}},
			want: xaiauth.CLIChatProxyBaseURL,
		},
		{
			name: "oauth using api uses official API",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  xaiauth.DefaultAPIBaseURL,
				"using_api": "true",
			}},
			want: xaiauth.DefaultAPIBaseURL,
		},
		{
			name: "API key uses official API",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "xai-key",
				"base_url": xaiauth.DefaultAPIBaseURL,
			}},
			want: xaiauth.DefaultAPIBaseURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := xaiChatBaseURL(tt.auth); got != tt.want {
				t.Fatalf("xaiChatBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestXAIRefreshPreservesEndpointMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("grant_type") != "refresh_token" {
			t.Fatalf("grant_type = %q, want refresh_token", r.FormValue("grant_type"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Metadata: map[string]any{
			"refresh_token":  "old-refresh",
			"token_endpoint": server.URL,
			"base_url":       xaiauth.DefaultAPIBaseURL,
			"auth_kind":      "oauth",
			"using_api":      true,
		},
		Attributes: map[string]string{"base_url": xaiauth.DefaultAPIBaseURL},
	}
	refreshed, err := NewXAIExecutor(nil).Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got, ok := refreshed.Metadata["using_api"].(bool); !ok || !got {
		t.Fatalf("metadata[using_api] = %#v, want true", refreshed.Metadata["using_api"])
	}
	if got := refreshed.Attributes["using_api"]; got != "true" {
		t.Fatalf("attributes[using_api] = %q, want true", got)
	}
}

func TestXAIResponsesEndpointModeHeaders(t *testing.T) {
	cliRequest := httptest.NewRequest(http.MethodPost, xaiauth.CLIChatProxyBaseURL+"/responses", nil)
	oauth := &cliproxyauth.Auth{Attributes: map[string]string{
		"auth_kind": "oauth",
		"base_url":  xaiauth.DefaultAPIBaseURL,
	}}
	applyXAIChatHeaders(cliRequest, oauth, "oauth-token", true, "")
	if got := cliRequest.Header.Get(xaiTokenAuthHeader); got != xaiTokenAuthValue {
		t.Fatalf("%s = %q, want %q", xaiTokenAuthHeader, got, xaiTokenAuthValue)
	}
	if got := cliRequest.Header.Get(xaiClientVersionHeader); got == "" {
		t.Fatalf("%s is empty, want CLI client identity", xaiClientVersionHeader)
	}

	apiRequest := httptest.NewRequest(http.MethodPost, xaiauth.DefaultAPIBaseURL+"/responses", nil)
	apiAuth := &cliproxyauth.Auth{Attributes: map[string]string{
		"auth_kind": "oauth",
		"base_url":  xaiauth.DefaultAPIBaseURL,
		"using_api": "true",
	}}
	applyXAIChatHeaders(apiRequest, apiAuth, "oauth-token", false, "")
	if got := apiRequest.Header.Get(xaiTokenAuthHeader); got != "" {
		t.Fatalf("%s = %q, want empty for API endpoint", xaiTokenAuthHeader, got)
	}
}

func TestXAICompactionKeepsOAuthOnAPIBaseURL(t *testing.T) {
	oauth := &cliproxyauth.Auth{Attributes: map[string]string{
		"auth_kind": "oauth",
		"base_url":  xaiauth.DefaultAPIBaseURL,
	}}
	if got := xaiAPIBaseURL(oauth); got != xaiauth.DefaultAPIBaseURL {
		t.Fatalf("xaiAPIBaseURL() = %q, want official API base URL", got)
	}
	request := httptest.NewRequest(http.MethodPost, xaiauth.DefaultAPIBaseURL+"/responses/compact", nil)
	applyXAIHeaders(request, oauth, "oauth-token", false, "")
	if got := request.Header.Get(xaiTokenAuthHeader); got != "" {
		t.Fatalf("compact request %s = %q, want API headers only", xaiTokenAuthHeader, got)
	}
}
