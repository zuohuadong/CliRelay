package executor

import (
	"strconv"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexFastModeServiceTier = "priority"

func applyCodexFastModeServiceTier(auth *cliproxyauth.Auth, rawJSON []byte) []byte {
	if len(rawJSON) == 0 || auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return rawJSON
	}

	normalized := rawJSON
	if gjson.GetBytes(normalized, "service_tier").Exists() {
		if updated, errDelete := sjson.DeleteBytes(normalized, "service_tier"); errDelete == nil {
			normalized = updated
		}
	}
	if !codexFastModeEnabled(auth) {
		return normalized
	}
	updated, errSet := sjson.SetBytes(normalized, "service_tier", codexFastModeServiceTier)
	if errSet != nil {
		return normalized
	}
	return updated
}

func codexFastModeEnabled(auth *cliproxyauth.Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	if auth.Attributes != nil {
		if parsed, ok := parseBoolString(auth.Attributes["codex_fast_mode"]); ok {
			return parsed
		}
	}
	if auth.Metadata == nil {
		return false
	}
	switch raw := auth.Metadata["codex_fast_mode"].(type) {
	case bool:
		return raw
	case string:
		parsed, ok := parseBoolString(raw)
		return ok && parsed
	default:
		return false
	}
}

func parseBoolString(value string) (bool, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false, false
	}
	parsed, errParse := strconv.ParseBool(trimmed)
	if errParse != nil {
		return false, false
	}
	return parsed, true
}
