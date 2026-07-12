package xai

import (
	"strconv"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// OAuthRecordFromTokenStorage converts an xAI OAuth token into the canonical
// persisted auth record. Endpoint mode is stored in metadata and attributes so
// it survives both auth-file reloads and refreshes.
func OAuthRecordFromTokenStorage(storage *TokenStorage, usingAPI bool) *coreauth.Auth {
	if storage == nil || strings.TrimSpace(storage.AccessToken) == "" {
		return nil
	}
	baseURL := strings.TrimSpace(storage.BaseURL)
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	fileName := CredentialFileName(storage.Email, storage.Subject)
	label := strings.TrimSpace(storage.Email)
	if label == "" {
		label = "xAI"
	}
	metadata := map[string]any{
		"type":           "xai",
		"access_token":   storage.AccessToken,
		"refresh_token":  storage.RefreshToken,
		"id_token":       storage.IDToken,
		"token_type":     storage.TokenType,
		"expires_in":     storage.ExpiresIn,
		"expired":        storage.Expire,
		"last_refresh":   storage.LastRefresh,
		"base_url":       baseURL,
		"redirect_uri":   storage.RedirectURI,
		"token_endpoint": storage.TokenEndpoint,
		"auth_kind":      "oauth",
		"using_api":      usingAPI,
	}
	if storage.Email != "" {
		metadata["email"] = storage.Email
	}
	if storage.Subject != "" {
		metadata["sub"] = storage.Subject
	}
	return &coreauth.Auth{
		ID:       fileName,
		Provider: "xai",
		FileName: fileName,
		Label:    label,
		Storage:  storage,
		Metadata: metadata,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  baseURL,
			"using_api": strconv.FormatBool(usingAPI),
		},
	}
}
