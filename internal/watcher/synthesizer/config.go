package synthesizer

import (
	"fmt"
	"strconv"
	"strings"

<<<<<<< HEAD
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
=======
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
>>>>>>> upstream/main
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ConfigSynthesizer generates Auth entries from configuration API keys.
// It handles Gemini, Claude, Codex, OpenAI-compat, and Vertex-compat providers.
type ConfigSynthesizer struct{}

// NewConfigSynthesizer creates a new ConfigSynthesizer instance.
func NewConfigSynthesizer() *ConfigSynthesizer {
	return &ConfigSynthesizer{}
}

// Synthesize generates Auth entries from config API keys.
func (s *ConfigSynthesizer) Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error) {
	out := make([]*coreauth.Auth, 0, 32)
	if ctx == nil || ctx.Config == nil {
		return out, nil
	}

	// Gemini API Keys
	out = append(out, s.synthesizeGeminiKeys(ctx)...)
	// Claude API Keys
	out = append(out, s.synthesizeClaudeKeys(ctx)...)
	// Codex API Keys
	out = append(out, s.synthesizeCodexKeys(ctx)...)
	// BigModel Coding Plan API Keys
	out = append(out, s.synthesizeBigModelCoding(ctx)...)
	// Astron Coding Plan API Keys
	out = append(out, s.synthesizeAstronCode(ctx)...)
	// OpenCode Go API Keys
	out = append(out, s.synthesizeOpenCodeGo(ctx)...)
	// OpenAI-compat
	out = append(out, s.synthesizeOpenAICompat(ctx)...)
	// Vertex-compat
	out = append(out, s.synthesizeVertexCompat(ctx)...)

	return out, nil
}

// synthesizeBigModelCoding creates Auth entries for Zhipu Coding Plan.
func (s *ConfigSynthesizer) synthesizeBigModelCoding(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0)
	for i := range cfg.BigModelCodingAPIKey {
		compat := &cfg.BigModelCodingAPIKey[i]
		if compat.Disabled {
			continue
		}
		prefix := strings.TrimSpace(compat.Prefix)
		providerName := config.DefaultBigModelCodingProviderName
		base := strings.TrimSpace(compat.BaseURL)
		if base == "" {
			base = config.DefaultBigModelCodingBaseURL
		}
		disableCooling := compat.DisableCooling

		createdEntries := 0
		for j := range compat.APIKeyEntries {
			entry := &compat.APIKeyEntries[j]
			if entry.Disabled {
				continue
			}
			key := strings.TrimSpace(entry.APIKey)
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			if pid := strings.TrimSpace(entry.ProxyID); pid != "" {
				if resolved := cfg.ResolveProxyURL(pid, ""); resolved != "" {
					proxyURL = resolved
				}
			}
			idKind := fmt.Sprintf("%s:%s", providerName, providerName)
			id, token := idGen.Next(idKind, key, base, proxyURL)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  providerName,
				"provider_key": providerName,
			}
			metadata := map[string]any{}
			if disableCooling {
				metadata["disable_cooling"] = true
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			if key != "" {
				attrs["api_key"] = key
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			addConfigHeadersToAttrs(entry.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   providerName,
				Label:      providerName,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				ProxyURL:   proxyURL,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
			createdEntries++
		}
		if createdEntries == 0 {
			idKind := fmt.Sprintf("%s:%s", providerName, providerName)
			id, token := idGen.Next(idKind, base)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  providerName,
				"provider_key": providerName,
			}
			metadata := map[string]any{}
			if disableCooling {
				metadata["disable_cooling"] = true
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   providerName,
				Label:      providerName,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
		}
	}
	return out
}

// synthesizeAstronCode creates Auth entries for iFlytek Astron Coding Plan.
func (s *ConfigSynthesizer) synthesizeAstronCode(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0)
	for i := range cfg.AstronCodeAPIKey {
		compat := &cfg.AstronCodeAPIKey[i]
		if compat.Disabled {
			continue
		}
		prefix := strings.TrimSpace(compat.Prefix)
		providerName := config.DefaultAstronCodeProviderName
		base := strings.TrimSpace(compat.BaseURL)
		if base == "" {
			base = config.DefaultAstronCodeBaseURL
		}
		disableCooling := compat.DisableCooling

		createdEntries := 0
		for j := range compat.APIKeyEntries {
			entry := &compat.APIKeyEntries[j]
			if entry.Disabled {
				continue
			}
			key := strings.TrimSpace(entry.APIKey)
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			if pid := strings.TrimSpace(entry.ProxyID); pid != "" {
				if resolved := cfg.ResolveProxyURL(pid, ""); resolved != "" {
					proxyURL = resolved
				}
			}
			idKind := fmt.Sprintf("%s:%s", providerName, providerName)
			id, token := idGen.Next(idKind, key, base, proxyURL)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  providerName,
				"provider_key": providerName,
			}
			metadata := map[string]any{}
			if disableCooling {
				metadata["disable_cooling"] = true
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			if key != "" {
				attrs["api_key"] = key
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			addConfigHeadersToAttrs(entry.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   providerName,
				Label:      providerName,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				ProxyURL:   proxyURL,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
			createdEntries++
		}
		if createdEntries == 0 {
			idKind := fmt.Sprintf("%s:%s", providerName, providerName)
			id, token := idGen.Next(idKind, base)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  providerName,
				"provider_key": providerName,
			}
			metadata := map[string]any{}
			if disableCooling {
				metadata["disable_cooling"] = true
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   providerName,
				Label:      providerName,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
		}
	}
	return out
}

// synthesizeOpenCodeGo creates Auth entries for OpenCode Go providers.
func (s *ConfigSynthesizer) synthesizeOpenCodeGo(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	const providerName = "opencode-go"
	out := make([]*coreauth.Auth, 0, len(cfg.OpenCodeGoKey))
	for i := range cfg.OpenCodeGoKey {
		entry := cfg.OpenCodeGoKey[i]
		key := strings.TrimSpace(entry.APIKey)
		if key == "" {
			continue
		}
		prefix := strings.TrimSpace(entry.Prefix)
		base := strings.TrimSpace(entry.BaseURL)
		if base == "" {
			base = "http://localhost:8080/v1"
		}
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		if pid := strings.TrimSpace(entry.ProxyID); pid != "" {
			if resolved := cfg.ResolveProxyURL(pid, ""); resolved != "" {
				proxyURL = resolved
			}
		}
		id, token := idGen.Next("opencode-go:apikey", key, base, proxyURL)
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:opencode-go[%s]", token),
			"base_url":     base,
			"compat_name":  providerName,
			"provider_key": providerName,
			"api_key":      key,
		}
		if entry.Priority != 0 {
			attrs["priority"] = strconv.Itoa(entry.Priority)
		}
		if entry.VisionModel != "" {
			attrs["vision_model"] = entry.VisionModel
		}
		if hash := diff.ComputeOpenCodeGoModelsHash(entry.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(entry.Headers, attrs)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   providerName,
			Label:      providerName,
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		out = append(out, a)
	}
	return out
}

// synthesizeGeminiKeys creates Auth entries for Gemini API keys.
func (s *ConfigSynthesizer) synthesizeGeminiKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.GeminiKey))
	for i := range cfg.GeminiKey {
		entry := cfg.GeminiKey[i]
		key := strings.TrimSpace(entry.APIKey)
		if key == "" {
			continue
		}
		prefix := strings.TrimSpace(entry.Prefix)
		base := strings.TrimSpace(entry.BaseURL)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		id, token := idGen.Next("gemini:apikey", key, base)
		attrs := map[string]string{
			"source":  fmt.Sprintf("config:gemini[%s]", token),
			"api_key": key,
		}
		metadata := map[string]any{}
		if entry.DisableCooling {
			metadata["disable_cooling"] = true
		}
		if entry.Priority != 0 {
			attrs["priority"] = strconv.Itoa(entry.Priority)
		}
		if base != "" {
			attrs["base_url"] = base
		}
		if hash := diff.ComputeGeminiModelsHash(entry.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(entry.Headers, attrs)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   "gemini",
			Label:      "gemini-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			Metadata:   metadata,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, entry.ExcludedModels, "apikey")
		if len(a.Metadata) == 0 {
			a.Metadata = nil
		}
		out = append(out, a)
	}
	return out
}

// synthesizeClaudeKeys creates Auth entries for Claude API keys.
func (s *ConfigSynthesizer) synthesizeClaudeKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.ClaudeKey))
	for i := range cfg.ClaudeKey {
		ck := cfg.ClaudeKey[i]
		key := strings.TrimSpace(ck.APIKey)
		if key == "" {
			continue
		}
		prefix := strings.TrimSpace(ck.Prefix)
		base := strings.TrimSpace(ck.BaseURL)
		id, token := idGen.Next("claude:apikey", key, base)
		attrs := map[string]string{
			"source":  fmt.Sprintf("config:claude[%s]", token),
			"api_key": key,
		}
		metadata := map[string]any{}
		if ck.DisableCooling {
			metadata["disable_cooling"] = true
		}
		if ck.Priority != 0 {
			attrs["priority"] = strconv.Itoa(ck.Priority)
		}
		if base != "" {
			attrs["base_url"] = base
		}
		if ck.RebuildMidSystemMessage {
			attrs["rebuild_mid_system_message"] = "true"
		}
		if hash := diff.ComputeClaudeModelsHash(ck.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(ck.Headers, attrs)
		proxyURL := strings.TrimSpace(ck.ProxyURL)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   "claude",
			Label:      "claude-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			Metadata:   metadata,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, ck.ExcludedModels, "apikey")
		if len(a.Metadata) == 0 {
			a.Metadata = nil
		}
		out = append(out, a)
	}
	return out
}

// synthesizeCodexKeys creates Auth entries for Codex API keys.
func (s *ConfigSynthesizer) synthesizeCodexKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.CodexKey))
	for i := range cfg.CodexKey {
		ck := cfg.CodexKey[i]
		key := strings.TrimSpace(ck.APIKey)
		if key == "" {
			continue
		}
		prefix := strings.TrimSpace(ck.Prefix)
		id, token := idGen.Next("codex:apikey", key, ck.BaseURL)
		attrs := map[string]string{
			"source":  fmt.Sprintf("config:codex[%s]", token),
			"api_key": key,
		}
		metadata := map[string]any{}
		if ck.DisableCooling {
			metadata["disable_cooling"] = true
		}
		if ck.Priority != 0 {
			attrs["priority"] = strconv.Itoa(ck.Priority)
		}
		if ck.BaseURL != "" {
			attrs["base_url"] = ck.BaseURL
		}
		if ck.Websockets {
			attrs["websockets"] = "true"
		}
		if hash := diff.ComputeCodexModelsHash(ck.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(ck.Headers, attrs)
		proxyURL := strings.TrimSpace(ck.ProxyURL)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   "codex",
			Label:      "codex-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			Metadata:   metadata,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, ck.ExcludedModels, "apikey")
		if len(a.Metadata) == 0 {
			a.Metadata = nil
		}
		out = append(out, a)
	}
	return out
}

// synthesizeOpenAICompat creates Auth entries for OpenAI-compatible providers.
func (s *ConfigSynthesizer) synthesizeOpenAICompat(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0)
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		prefix := strings.TrimSpace(compat.Prefix)
		providerName := strings.ToLower(strings.TrimSpace(compat.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		internalProviderKey := util.OpenAICompatibleProviderKey(providerName)
		base := strings.TrimSpace(compat.BaseURL)
		disableCooling := compat.DisableCooling

		// Handle new APIKeyEntries format (preferred)
		createdEntries := 0
		for j := range compat.APIKeyEntries {
			entry := &compat.APIKeyEntries[j]
			if entry.Disabled {
				continue
			}
			key := strings.TrimSpace(entry.APIKey)
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			if pid := strings.TrimSpace(entry.ProxyID); pid != "" {
				if resolved := cfg.ResolveProxyURL(pid, ""); resolved != "" {
					proxyURL = resolved
				}
			}
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			id, token := idGen.Next(idKind, key, base, proxyURL)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  compat.Name,
				"provider_key": internalProviderKey,
			}
			metadata := map[string]any{}
			if disableCooling {
				metadata["disable_cooling"] = true
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			if key != "" {
				attrs["api_key"] = key
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			addConfigHeadersToAttrs(entry.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   internalProviderKey,
				Label:      compat.Name,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				ProxyURL:   proxyURL,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
			createdEntries++
		}
		// Fallback: create entry without API key if no APIKeyEntries
		if createdEntries == 0 {
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			id, token := idGen.Next(idKind, base)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  compat.Name,
				"provider_key": internalProviderKey,
			}
			metadata := map[string]any{}
			if disableCooling {
				metadata["disable_cooling"] = true
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   internalProviderKey,
				Label:      compat.Name,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
		}
	}
	return out
}

// synthesizeVertexCompat creates Auth entries for Vertex-compatible providers.
func (s *ConfigSynthesizer) synthesizeVertexCompat(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.VertexCompatAPIKey))
	for i := range cfg.VertexCompatAPIKey {
		compat := &cfg.VertexCompatAPIKey[i]
		providerName := "vertex"
		base := strings.TrimSpace(compat.BaseURL)

		key := strings.TrimSpace(compat.APIKey)
		prefix := strings.TrimSpace(compat.Prefix)
		proxyURL := strings.TrimSpace(compat.ProxyURL)
		idKind := "vertex:apikey"
		id, token := idGen.Next(idKind, key, base, proxyURL)
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:vertex-apikey[%s]", token),
			"base_url":     base,
			"provider_key": providerName,
		}
		if compat.Priority != 0 {
			attrs["priority"] = strconv.Itoa(compat.Priority)
		}
		if key != "" {
			attrs["api_key"] = key
		}
		if hash := diff.ComputeVertexCompatModelsHash(compat.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(compat.Headers, attrs)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   providerName,
			Label:      "vertex-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, compat.ExcludedModels, "apikey")
		out = append(out, a)
	}
	return out
}
