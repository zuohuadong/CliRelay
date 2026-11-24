package auth

import "strings"

// SupportsCodexAlphaSearch keeps this independent protocol off generic Responses
// channels. API keys require an operator opt-in; OAuth uses the Codex endpoint.
func SupportsCodexAlphaSearch(a *Auth) bool {
	if a == nil || a.Provider != "codex" {
		return false
	}
	if strings.TrimSpace(a.Attributes["api_key"]) != "" {
		enabled := strings.EqualFold(strings.TrimSpace(a.Attributes[AttributeCodexAlphaSearch]), "true") ||
			strings.EqualFold(strings.TrimSpace(a.Attributes["alpha_search"]), "true")
		return enabled && strings.TrimSpace(a.Attributes["base_url"]) != ""
	}
	token, _ := a.Metadata["access_token"].(string)
	return strings.TrimSpace(token) != ""
}
