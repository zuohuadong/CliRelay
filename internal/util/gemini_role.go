package util

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeGeminiContentRole maps a message role onto one of the two roles
// Gemini accepts in `contents[].role`.
//
// Gemini rejects anything else with a bare 400 INVALID_ARGUMENT that names no
// field, which makes the cause expensive to find. The Claude translators rewrite
// "assistant" and pass every other role through untouched, so a "system" entry
// inside `messages` — which Claude Code sends alongside the top-level `system`
// field — reached the upstream verbatim and failed the whole request.
//
// An allowlist rather than a list of roles to rewrite: a role this code has
// never heard of becomes "user" and its text still reaches the model, instead
// of being forwarded as an invalid value.
func NormalizeGeminiContentRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant", "model":
		return "model"
	default:
		return "user"
	}
}

// NormalizeGeminiContentRoles rewrites every contents[].role in an Antigravity
// or Gemini request payload so the upstream cannot be sent a role it rejects.
func NormalizeGeminiContentRoles(payload string, contentsPath string) string {
	contents := gjson.Get(payload, contentsPath)
	if !contents.IsArray() {
		return payload
	}
	updated := payload
	index := 0
	contents.ForEach(func(_, content gjson.Result) bool {
		role := content.Get("role")
		if role.Type == gjson.String {
			if normalized := NormalizeGeminiContentRole(role.String()); normalized != role.String() {
				updated, _ = sjson.Set(updated, fmt.Sprintf("%s.%d.role", contentsPath, index), normalized)
			}
		}
		index++
		return true
	})
	return updated
}

// NormalizeGeminiContentRolesBytes rewrites every contents[].role in an Antigravity
// or Gemini byte payload.
func NormalizeGeminiContentRolesBytes(payload []byte, contentsPath string) []byte {
	contents := gjson.GetBytes(payload, contentsPath)
	if !contents.IsArray() {
		return payload
	}
	updated := payload
	index := 0
	contents.ForEach(func(_, content gjson.Result) bool {
		role := content.Get("role")
		if role.Type == gjson.String {
			if normalized := NormalizeGeminiContentRole(role.String()); normalized != role.String() {
				updated, _ = sjson.SetBytes(updated, fmt.Sprintf("%s.%d.role", contentsPath, index), normalized)
			}
		}
		index++
		return true
	})
	return updated
}
