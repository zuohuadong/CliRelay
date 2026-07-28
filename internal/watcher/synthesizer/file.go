package synthesizer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// FileSynthesizer generates Auth entries from OAuth JSON files.
// It handles file-based authentication.
type FileSynthesizer struct{}

// NewFileSynthesizer creates a new FileSynthesizer instance.
func NewFileSynthesizer() *FileSynthesizer {
	return &FileSynthesizer{}
}

// Synthesize generates Auth entries from auth files in the auth directory.
func (s *FileSynthesizer) Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error) {
	out := make([]*coreauth.Auth, 0, 16)
	if ctx == nil || ctx.AuthDir == "" {
		return out, nil
	}

	entries, err := os.ReadDir(ctx.AuthDir)
	if err != nil {
		// Not an error if directory doesn't exist
		return out, nil
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		full := filepath.Join(ctx.AuthDir, name)
		data, errRead := os.ReadFile(full)
		if errRead != nil || len(data) == 0 {
			continue
		}
		auths := synthesizeFileAuths(ctx, full, data)
		if len(auths) == 0 {
			continue
		}
		out = append(out, auths...)
	}
	return out, nil
}

// SynthesizeAuthFile generates Auth entries for one auth JSON file payload.
// It shares exactly the same mapping behavior as FileSynthesizer.Synthesize.
func SynthesizeAuthFile(ctx *SynthesisContext, fullPath string, data []byte) []*coreauth.Auth {
	return synthesizeFileAuths(ctx, fullPath, data)
}

func synthesizeFileAuths(ctx *SynthesisContext, fullPath string, data []byte) []*coreauth.Auth {
	if ctx == nil || len(data) == 0 {
		return nil
	}
	now := ctx.Now
	cfg := ctx.Config
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		return nil
	}
	t, _ := metadata["type"].(string)
	if strings.TrimSpace(t) == "" {
		t, _ = metadata["provider"].(string)
	}
	provider := strings.ToLower(strings.TrimSpace(t))
	if provider == "gemini" {
		provider = "gemini-cli"
	}
	// Codex CLI native exports wrap tokens under accounts[].credentials
	// instead of flattening them at the top level. Expand the bundle into one
	// metadata map per account so the rest of the synthesis (account_id/identity
	// resolution, JWT plan_type, excluded models) runs for each account.
	// expandCodexBundle returns nil for flat auth files (no accounts wrapper).
	var codexAccounts []map[string]any
	if provider == "codex" {
		codexAccounts = expandCodexBundle(metadata)
	}
	if ctx.PluginAuthParser != nil {
		auths, handled, errParse := parsePluginFileAuths(ctx.PluginAuthParser, pluginapi.AuthParseRequest{
			Provider: provider,
			Path:     fullPath,
			FileName: filepath.Base(fullPath),
			RawJSON:  data,
		})
		if errParse == nil && handled {
			auths = compactPluginAuths(auths)
			if len(auths) == 0 {
				return nil
			}
			perAccountExcluded := extractExcludedModelsFromMetadata(metadata)
			perAccountModelAliases := extractOAuthModelAliasesFromMetadata(metadata)
			disabled, _ := metadata["disabled"].(bool)
			for index, auth := range auths {
				if auth == nil {
					continue
				}
				if len(auths) > 1 {
					coreauth.MarkPluginVirtualAuth(auth, fullPath, index)
				}
				auth.CreatedAt = now
				auth.UpdatedAt = now
				if auth.Attributes == nil {
					auth.Attributes = make(map[string]string)
				}
				auth.Attributes[coreauth.AttributePath] = fullPath
				auth.Attributes[coreauth.AttributeSource] = fullPath
				auth.Attributes[coreauth.AttributeSourceBackend] = coreauth.AuthSourceFile
				if disabled {
					auth.Disabled = true
					auth.Status = coreauth.StatusDisabled
					if auth.Metadata == nil {
						auth.Metadata = make(map[string]any)
					}
					auth.Metadata["disabled"] = true
				}
				coreauth.SetOAuthModelAliasesAttribute(auth, perAccountModelAliases)
				ApplyAuthExcludedModelsMeta(auth, cfg, perAccountExcluded, "oauth")
				coreauth.ApplyCustomHeadersFromMetadata(auth)
			}
			return auths
		}
	}
	if provider == "" || provider == "gemini-cli" {
		return nil
	}
	// Use relative path under authDir as ID to stay consistent with the file-based token store.
	baseID := fullPath
	if strings.TrimSpace(ctx.AuthDir) != "" {
		if rel, errRel := filepath.Rel(ctx.AuthDir, fullPath); errRel == nil && rel != "" {
			baseID = rel
		}
	}
	if runtime.GOOS == "windows" {
		baseID = strings.ToLower(baseID)
	}
	// When a Codex CLI native export expands into multiple accounts, synthesize
	// one auth per account. Flat auth files (codexAccounts == nil) keep the
	// original single-metadata path so behavior is unchanged.
	if codexAccounts != nil {
		out := make([]*coreauth.Auth, 0, len(codexAccounts))
		multi := len(codexAccounts) > 1
		for index, acct := range codexAccounts {
			auth := synthesizeOneCodexAuth(ctx, fullPath, baseID, provider, acct, now)
			if auth == nil {
				continue
			}
			if multi {
				auth.ID = baseID + "#" + strconv.Itoa(index)
				coreauth.MarkFileBundleAuth(auth, fullPath, index)
			}
			out = append(out, auth)
		}
		return out
	}
	auth := synthesizeOneCodexAuth(ctx, fullPath, baseID, provider, metadata, now)
	if auth == nil {
		return nil
	}
	return []*coreauth.Auth{auth}
}

// synthesizeOneCodexAuth builds a single Auth from one metadata map. It is the
// shared implementation for both flat auth files and expanded bundle accounts so
// per-account fields (email, priority, id_token, excluded models) resolve
// against the correct account.
func synthesizeOneCodexAuth(ctx *SynthesisContext, fullPath, baseID, provider string, metadata map[string]any, now time.Time) *coreauth.Auth {
	if metadata == nil {
		return nil
	}
	cfg := ctx.Config
	label := provider
	if email, _ := metadata["email"].(string); email != "" {
		label = email
	}
	compatName := ""
	if provider == "openai-compatibility" {
		compatName, _ = metadata["compat_name"].(string)
		compatName = strings.TrimSpace(compatName)
		if compatName == "" {
			compatName, _ = metadata["name"].(string)
			compatName = strings.TrimSpace(compatName)
		}
		if compatName != "" {
			label = compatName
		}
	}
	id := baseID
	proxyURL := ""
	if p, ok := metadata["proxy_url"].(string); ok {
		proxyURL = p
	}
	prefix := ""
	if rawPrefix, ok := metadata["prefix"].(string); ok {
		trimmed := strings.TrimSpace(rawPrefix)
		trimmed = strings.Trim(trimmed, "/")
		if trimmed != "" && !strings.Contains(trimmed, "/") {
			prefix = trimmed
		}
	}
	disabled, _ := metadata["disabled"].(bool)
	status := coreauth.StatusActive
	if disabled {
		status = coreauth.StatusDisabled
	}
	perAccountExcluded := extractExcludedModelsFromMetadata(metadata)
	perAccountModelAliases := extractOAuthModelAliasesFromMetadata(metadata)
	a := &coreauth.Auth{
		ID:       id,
		Provider: provider,
		Label:    label,
		Prefix:   prefix,
		Status:   status,
		Disabled: disabled,
		Attributes: map[string]string{
			coreauth.AttributeSource:        fullPath,
			coreauth.AttributePath:          fullPath,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		ProxyURL:  proxyURL,
		Metadata:  metadata,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if provider == "openai-compatibility" {
		if compatName != "" {
			a.Attributes["compat_name"] = compatName
			a.Attributes["provider_key"] = compatName
		}
		if baseURL, _ := metadata["base_url"].(string); strings.TrimSpace(baseURL) != "" {
			a.Attributes["base_url"] = strings.TrimSpace(baseURL)
		}
		if apiKey, _ := metadata["api_key"].(string); strings.TrimSpace(apiKey) != "" {
			a.Attributes["api_key"] = strings.TrimSpace(apiKey)
			a.Attributes["auth_kind"] = "api_key"
		}
	}
	if rawPriority, ok := metadata["priority"]; ok {
		switch v := rawPriority.(type) {
		case float64:
			a.Attributes["priority"] = strconv.Itoa(int(v))
		case string:
			priority := strings.TrimSpace(v)
			if _, errAtoi := strconv.Atoi(priority); errAtoi == nil {
				a.Attributes["priority"] = priority
			}
		}
	}
	if rawNote, ok := metadata["note"]; ok {
		if note, isStr := rawNote.(string); isStr {
			if trimmed := strings.TrimSpace(note); trimmed != "" {
				a.Attributes["note"] = trimmed
			}
		}
	}
	coreauth.ApplyCustomHeadersFromMetadata(a)
	coreauth.SetOAuthModelAliasesAttribute(a, perAccountModelAliases)
	authKind := "oauth"
	if provider == "openai-compatibility" && strings.TrimSpace(a.Attributes["api_key"]) != "" {
		authKind = "api_key"
	}
	ApplyAuthExcludedModelsMeta(a, cfg, perAccountExcluded, authKind)
	if provider == "codex" {
		if idTokenRaw, ok := metadata["id_token"].(string); ok && strings.TrimSpace(idTokenRaw) != "" {
			if claims, errParse := codex.ParseJWTToken(idTokenRaw); errParse == nil && claims != nil {
				if pt := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); pt != "" {
					a.Attributes["plan_type"] = pt
				}
			}
		}
		if accountID := codex.AccountIDFromMetadata(metadata); accountID != "" {
			if identity, errIdentity := egress.StableIdentity(accountID); errIdentity == nil {
				a.Attributes["stable_identity"] = identity
			}
		}
	}
	return a
}

func parsePluginFileAuths(parser PluginAuthParser, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	if parser == nil {
		return nil, false, nil
	}
	if multiParser, ok := parser.(PluginMultiAuthParser); ok {
		return multiParser.ParseAuths(context.Background(), req)
	}
	auth, handled, errParse := parser.ParseAuth(context.Background(), req)
	if errParse != nil || !handled || auth == nil {
		return nil, handled, errParse
	}
	return []*coreauth.Auth{auth}, true, nil
}

func compactPluginAuths(auths []*coreauth.Auth) []*coreauth.Auth {
	if len(auths) == 0 {
		return nil
	}
	out := auths[:0]
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		out = append(out, auth)
	}
	return out
}

// extractOAuthModelAliasesFromMetadata reads per-account model aliases from OAuth JSON metadata.
// Supports both "model_aliases" and "model-aliases" keys.
func extractOAuthModelAliasesFromMetadata(metadata map[string]any) []config.OAuthModelAlias {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata["model_aliases"]
	if !ok {
		raw, ok = metadata["model-aliases"]
	}
	if !ok || raw == nil {
		return nil
	}
	data, errMarshal := json.Marshal(raw)
	if errMarshal != nil {
		return nil
	}
	var aliases []config.OAuthModelAlias
	if errUnmarshal := json.Unmarshal(data, &aliases); errUnmarshal != nil {
		return nil
	}
	cfg := config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"auth": aliases,
		},
	}
	cfg.SanitizeOAuthModelAlias()
	return cfg.OAuthModelAlias["auth"]
}

// extractExcludedModelsFromMetadata reads per-account excluded models from the OAuth JSON metadata.
// Supports both "excluded_models" and "excluded-models" keys, and accepts both []string and []interface{}.
func extractExcludedModelsFromMetadata(metadata map[string]any) []string {
	if metadata == nil {
		return nil
	}
	// Try both key formats
	raw, ok := metadata["excluded_models"]
	if !ok {
		raw, ok = metadata["excluded-models"]
	}
	if !ok || raw == nil {
		return nil
	}
	var stringSlice []string
	switch v := raw.(type) {
	case []string:
		stringSlice = v
	case []interface{}:
		stringSlice = make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				stringSlice = append(stringSlice, s)
			}
		}
	default:
		return nil
	}
	result := make([]string, 0, len(stringSlice))
	for _, s := range stringSlice {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// codexBundleCredentialKeys are the credential fields promoted from a Codex CLI
// native/Agent Identity export (accounts[].credentials) to top-level metadata
// so the rest of the synthesis path resolves them like a flat auth file.
var codexBundleCredentialKeys = []string{
	"account_id", "id_token", "access_token", "refresh_token", "email",
	"plan_type", "expired", "last_refresh", "token_type",
	"chatgpt_account_id", "chatgpt_user_id", "workspace_id", "task_id",
	"agent_private_key", "agent_runtime_id", "auth_mode",
}

// expandCodexBundle detects the Codex CLI native/Agent Identity export format
// from a non-empty accounts[].credentials array and returns one metadata map
// per account with that account's credential fields promoted to the top level.
// The returned maps share the top-level metadata as a base and overlay each
// account's credentials (and per-account fields like priority/name/email) so the
// rest of the synthesis (account_id/identity resolution, JWT plan_type, excluded
// models) runs for every account.
//
// Returns nil for flat auth files (no accounts[].credentials wrapper) so the
// caller keeps the original single-metadata path unchanged. Some exporters omit
// the top-level version field, so the nested credential shape is authoritative.
func expandCodexBundle(metadata map[string]any) []map[string]any {
	if metadata == nil {
		return nil
	}
	accounts, ok := metadata["accounts"].([]any)
	if !ok || len(accounts) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(accounts))
	for _, entry := range accounts {
		account, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		credentials, ok := account["credentials"].(map[string]any)
		if !ok || len(credentials) == 0 {
			continue
		}
		// Clone the top-level metadata so each account gets its own map, then
		// promote this account's credential fields (overriding top-level copies).
		clone := make(map[string]any, len(metadata)+len(credentials)+4)
		for key, value := range metadata {
			clone[key] = value
		}
		for _, key := range codexBundleCredentialKeys {
			if value, ok := credentials[key]; ok {
				clone[key] = value
			}
		}
		// Promote per-account scalar fields that the synthesizer reads at the
		// top level (priority/name/email/disabled). credentials.email already
		// covers email; cover the rest here.
		for _, key := range []string{"priority", "name", "disabled", "proxy_url", "prefix", "note"} {
			if value, ok := account[key]; ok {
				if key == "disabled" {
					sourceDisabled, _ := clone[key].(bool)
					accountDisabled, _ := value.(bool)
					clone[key] = sourceDisabled || accountDisabled
					continue
				}
				clone[key] = value
			}
		}
		// Keep the raw accounts slice available for downstream readers, but the
		// promoted credential fields are authoritative for this account.
		out = append(out, clone)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
