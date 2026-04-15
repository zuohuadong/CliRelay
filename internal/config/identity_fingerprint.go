package config

import "strings"

// SanitizeIdentityFingerprint normalizes provider identity fingerprint config.
func (cfg *Config) SanitizeIdentityFingerprint() {
	if cfg == nil {
		return
	}
	cfg.IdentityFingerprint.Codex = NormalizeCodexIdentityFingerprint(cfg.IdentityFingerprint.Codex)
}

// NormalizeCodexIdentityFingerprint trims user input and applies safe defaults
// for fields that participate in Codex upstream identity.
func NormalizeCodexIdentityFingerprint(in CodexIdentityFingerprintConfig) CodexIdentityFingerprintConfig {
	out := in
	out.UserAgent = strings.TrimSpace(out.UserAgent)
	out.Version = strings.TrimSpace(out.Version)
	out.Originator = strings.TrimSpace(out.Originator)
	out.WebsocketBeta = strings.TrimSpace(out.WebsocketBeta)
	out.SessionMode = strings.TrimSpace(strings.ToLower(out.SessionMode))
	out.SessionID = strings.TrimSpace(out.SessionID)

	if out.UserAgent == "" {
		out.UserAgent = DefaultCodexFingerprintUserAgent
	}
	if out.Version == "" {
		out.Version = DefaultCodexFingerprintVersion
	}
	if out.Originator == "" {
		out.Originator = DefaultCodexFingerprintOriginator
	}
	if out.WebsocketBeta == "" {
		out.WebsocketBeta = DefaultCodexFingerprintWebsocketBeta
	}
	if out.SessionMode == "" {
		out.SessionMode = DefaultCodexFingerprintSessionMode
	}
	if out.SessionMode != "server-stable" && out.SessionMode != "fixed" && out.SessionMode != "per-request" {
		out.SessionMode = DefaultCodexFingerprintSessionMode
	}

	if len(out.CustomHeaders) > 0 {
		cleaned := make(map[string]string, len(out.CustomHeaders))
		for key, value := range out.CustomHeaders {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "" || value == "" {
				continue
			}
			cleaned[key] = value
		}
		out.CustomHeaders = cleaned
	} else {
		out.CustomHeaders = map[string]string{}
	}
	return out
}
