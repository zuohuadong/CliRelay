package cliproxy

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// registerModelsForAuth (re)binds provider models in the global registry using the core auth ID as client identifier.
func (s *Service) registerModelsForAuth(ctx context.Context, a *coreauth.Auth) {
	s.registerModelsForAuthWithCache(ctx, a, nil)
}

func (s *Service) registerModelsForAuthWithCache(ctx context.Context, a *coreauth.Auth, compatCache *openAICompatibilityRegistrationCache) {
	if a == nil || a.ID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}
	if a.Disabled {
		GlobalModelRegistry().UnregisterClient(a.ID)
		return
	}
	authKind := a.AuthKind()
	// Unregister legacy client ID (if present) to avoid double counting
	if a.Runtime != nil {
		if idGetter, ok := a.Runtime.(interface{ GetClientID() string }); ok {
			if rid := idGetter.GetClientID(); rid != "" && rid != a.ID {
				GlobalModelRegistry().UnregisterClient(rid)
			}
		}
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	compatProviderKey, compatDisplayName, compatDetected := openAICompatInfoFromAuth(a)
	if compatDetected {
		switch strings.ToLower(strings.TrimSpace(compatProviderKey)) {
		case "bigmodel-coding", "astron-code", "opencode-go":
			provider = strings.ToLower(strings.TrimSpace(compatProviderKey))
		default:
			provider = "openai-compatibility"
		}
	}
	excluded := s.oauthExcludedModels(provider, authKind)
	// The synthesizer pre-merges per-account and global exclusions into the "excluded_models" attribute.
	// If this attribute is present, it represents the complete list of exclusions and overrides the global config.
	if a.Attributes != nil {
		if val, ok := a.Attributes["excluded_models"]; ok && strings.TrimSpace(val) != "" {
			excluded = strings.Split(val, ",")
		}
	}
	if s.tryRegisterPluginModelsForAuth(ctx, a, provider, authKind, excluded) {
		return
	}
	if ctx.Err() != nil {
		return
	}
	var models []*ModelInfo
	switch provider {
	case constant.Gemini:
		models = registry.GetGeminiModels()
		if entry := s.resolveConfigGeminiKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildGeminiConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case constant.GeminiInteractions:
		models = registry.GetGeminiModels()
		if entry := s.resolveConfigInteractionsKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildGeminiConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "vertex":
		// Vertex AI Gemini supports the same model identifiers as Gemini.
		models = registry.GetGeminiVertexModels()
		if entry := s.resolveConfigVertexCompatKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildVertexCompatConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "aistudio":
		models = registry.GetAIStudioModels()
		models = applyExcludedModels(models, excluded)
	case "antigravity":
		fetched := s.fetchAntigravityModelsForAuth(ctx, a)
		models = mergeAntigravityFetchedModels(registry.GetAntigravityModels(), fetched.Models, fetched.Hints)
		models = applyExcludedModels(models, excluded)
	case "claude":
		models = registry.GetClaudeModels()
		if entry := s.resolveConfigClaudeKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildClaudeConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "codex":
		if authKind == "apikey" {
			if entry := s.resolveConfigCodexKey(a); entry != nil {
				models = buildCodexConfigModels(entry)
				excluded = entry.ExcludedModels
			}
			models = applyExcludedModels(models, excluded)
			break
		}
		codexPlanType := ""
		if a.Attributes != nil {
			codexPlanType = strings.TrimSpace(a.Attributes["plan_type"])
		}
		switch strings.ToLower(codexPlanType) {
		case "pro":
			models = registry.GetCodexProModels()
		case "plus":
			models = registry.GetCodexPlusModels()
		case "team", "business", "go":
			models = registry.GetCodexTeamModels()
		case "free":
			models = registry.GetCodexFreeModels()
		default:
			models = registry.GetCodexProModels()
		}
		if entry := s.resolveConfigCodexKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildCodexConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "kimi":
		models = registry.GetKimiModels()
		models = applyExcludedModels(models, excluded)
	case "xai":
		models = s.fetchXAIModelsForAuth(ctx, a)
		if entry := s.resolveConfigXAIKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildXAIConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "bigmodel-coding", "astron-code":
		if s.registerDedicatedOpenAICompatibilityMetadataModels(a, provider) {
			return
		}
		var entries []config.OpenAICompatibility
		if s.cfg != nil && provider == "bigmodel-coding" {
			entries = s.cfg.BigModelCodingAPIKey
		} else if s.cfg != nil {
			entries = s.cfg.AstronCodeAPIKey
		}
		for i := range entries {
			if entries[i].Disabled {
				continue
			}
			models = buildOpenAICompatibilityConfigModels(&entries[i])
			if len(models) > 0 {
				s.registerResolvedModelsForAuth(a, provider, applyModelPrefixes(models, a.Prefix, s.cfg != nil && s.cfg.ForceModelPrefix))
			} else {
				GlobalModelRegistry().UnregisterClient(a.ID)
			}
			return
		}
		GlobalModelRegistry().UnregisterClient(a.ID)
		return
	case "agnes":
		if s.cfg != nil {
			for i := range s.cfg.AgnesAPIKey {
				entry := &s.cfg.AgnesAPIKey[i]
				if entry.Disabled || !openAICompatibilityHasEnabledAPIKey(entry) {
					continue
				}
				models = buildOpenAICompatibilityConfigModels(entry)
				if len(models) > 0 {
					s.registerResolvedModelsForAuth(a, "agnes", applyModelPrefixes(models, a.Prefix, s.cfg.ForceModelPrefix))
				} else {
					GlobalModelRegistry().UnregisterClient(a.ID)
				}
				return
			}
		}
		GlobalModelRegistry().UnregisterClient(a.ID)
		return
	case "opencode-go":
		models = registry.GetOpenCodeGoModels()
		if entry := s.resolveConfigOpenCodeGoKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildOpenCodeGoConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "bedrock":
		models = registry.GetBedrockModels()
		if entry := s.resolveConfigBedrockKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildBedrockConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	default:
		// Handle OpenAI-compatibility providers by name using config
		if s.cfg != nil {
			providerKey := provider
			compatName := strings.TrimSpace(a.Provider)
			isCompatAuth := false
			if compatDetected {
				if compatProviderKey != "" {
					providerKey = compatProviderKey
				}
				if compatDisplayName != "" {
					compatName = compatDisplayName
				}
				isCompatAuth = true
			}
			if strings.EqualFold(providerKey, "openai-compatibility") {
				isCompatAuth = true
				if a.Attributes != nil {
					if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
						compatName = v
					}
					if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
						providerKey = strings.ToLower(v)
						isCompatAuth = true
					}
				}
				if providerKey == "openai-compatibility" && compatName != "" {
					providerKey = strings.ToLower(compatName)
				}
			} else if a.Attributes != nil {
				if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
					compatName = v
					isCompatAuth = true
				}
				if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
					providerKey = strings.ToLower(v)
					isCompatAuth = true
				}
			}
			registerCompat := func(compat *config.OpenAICompatibility) bool {
				if compat == nil || compat.Disabled {
					return false
				}
				isCompatAuth = true
				ms := buildOpenAICompatibilityConfigModels(compat)
				if providerKey == "" {
					providerKey = "openai-compatibility"
				}
				if len(ms) > 0 {
					ms = applyModelOverrides(s.cfg, providerKey, authKind, ms)
					ms = s.appendPluginModels(providerKey, ms)
					s.registerResolvedModelsForAuth(a, providerKey, applyModelPrefixes(ms, a.Prefix, s.cfg.ForceModelPrefix))
				} else {
					ms = s.appendPluginModels(providerKey, nil)
					if len(ms) > 0 {
						s.registerResolvedModelsForAuth(a, providerKey, applyModelPrefixes(ms, a.Prefix, s.cfg.ForceModelPrefix))
					} else {
						GlobalModelRegistry().UnregisterClient(a.ID)
					}
				}
				return true
			}
			if cached, ok := compatCache.lookup(a, compatName); ok {
				isCompatAuth = true
				if providerKey == "" {
					providerKey = cached.providerKey
				}
				if providerKey == "" {
					providerKey = "openai-compatibility"
				}
				ms := cached.models
				if len(ms) > 0 {
					ms = s.appendPluginModels(providerKey, ms)
					s.registerResolvedModelsForAuth(a, providerKey, applyModelPrefixes(ms, a.Prefix, s.cfg.ForceModelPrefix))
				} else {
					ms = s.appendPluginModels(providerKey, nil)
					if len(ms) > 0 {
						s.registerResolvedModelsForAuth(a, providerKey, applyModelPrefixes(ms, a.Prefix, s.cfg.ForceModelPrefix))
					} else {
						GlobalModelRegistry().UnregisterClient(a.ID)
					}
				}
				return
			}
			if indexed := configEntryForAuthIndex(a, s.cfg.OpenAICompatibility); indexed != nil && registerCompat(indexed) {
				return
			}
			for i := range s.cfg.OpenAICompatibility {
				compat := &s.cfg.OpenAICompatibility[i]
				if strings.EqualFold(compat.Name, compatName) && registerCompat(compat) {
					return
				}
			}
			if isCompatAuth {
				models = buildOpenAICompatibilityAuthMetadataModels(a, compatName)
				if len(models) > 0 {
					if providerKey == "" {
						providerKey = "openai-compatibility"
					}
					models = s.appendPluginModels(providerKey, models)
					s.registerResolvedModelsForAuth(a, providerKey, applyModelPrefixes(models, a.Prefix, s.cfg != nil && s.cfg.ForceModelPrefix))
					return
				}
				models = s.appendPluginModels(providerKey, nil)
				if len(models) > 0 {
					s.registerResolvedModelsForAuth(a, providerKey, applyModelPrefixes(models, a.Prefix, s.cfg != nil && s.cfg.ForceModelPrefix))
				} else {
					// No matching provider found or models removed entirely; drop any prior registration.
					GlobalModelRegistry().UnregisterClient(a.ID)
				}
				return
			}
		}
	}
	models = applyModelOverrides(s.cfg, provider, authKind, models)
	if ctx.Err() != nil {
		return
	}
	models = applyOAuthModelAliasForAuth(s.cfg, provider, authKind, a.Attributes, models)
	if ctx.Err() != nil {
		return
	}
	models = applyModelOverrides(s.cfg, provider, authKind, models)
	key := provider
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(a.Provider))
	}
	models = s.appendPluginModels(key, models)
	if len(models) > 0 {
		s.registerResolvedModelsForAuth(a, key, applyModelPrefixes(models, a.Prefix, s.cfg != nil && s.cfg.ForceModelPrefix))
		return
	}

	GlobalModelRegistry().UnregisterClient(a.ID)
}

// refreshModelRegistrationForAuth re-applies the latest model registration for
// one auth and reconciles any concurrent auth changes that race with the
// refresh. Callers are expected to pre-filter provider membership.
//
// Re-registration is deliberate: registry cooldown/suspension state is treated
// as part of the previous registration snapshot and is cleared when the auth is
// rebound to the refreshed model catalog.
func (s *Service) refreshModelRegistrationForAuth(current *coreauth.Auth) bool {
	return s.refreshModelRegistrationForAuthWithContext(context.Background(), current, nil)
}

func (s *Service) refreshModelRegistrationForAuthWithCache(current *coreauth.Auth, compatCache *openAICompatibilityRegistrationCache) bool {
	return s.refreshModelRegistrationForAuthWithContext(context.Background(), current, compatCache)
}

func (s *Service) refreshModelRegistrationForAuthWithContext(ctx context.Context, current *coreauth.Auth, compatCache *openAICompatibilityRegistrationCache) bool {
	if s == nil || s.coreManager == nil || current == nil || current.ID == "" {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	if !current.Disabled {
		s.ensureExecutorsForAuthWithContext(ctx, current, false)
	}
	s.registerModelsForAuthWithCache(ctx, current, compatCache)
	s.coreManager.ReconcileRegistryModelStates(ctx, current.ID)
	if ctx.Err() != nil {
		return false
	}

	latest, ok := s.latestAuthForModelRegistration(current.ID)
	if !ok || latest.Disabled {
		GlobalModelRegistry().UnregisterClient(current.ID)
		s.coreManager.RefreshSchedulerEntry(current.ID)
		return false
	}

	// Re-apply the latest auth snapshot so concurrent auth updates cannot leave
	// stale model registrations behind. This may duplicate registration work when
	// no auth fields changed, but keeps the refresh path simple and correct.
	s.ensureExecutorsForAuthWithContext(ctx, latest, false)
	s.registerModelsForAuthWithCache(ctx, latest, compatCache)
	if ctx.Err() != nil {
		return false
	}
	s.coreManager.ReconcileRegistryModelStates(ctx, latest.ID)
	s.coreManager.RefreshSchedulerEntry(current.ID)
	return true
}

// latestAuthForModelRegistration returns the latest auth snapshot regardless of
// provider membership. Callers use this after a registration attempt to restore
// whichever state currently owns the client ID in the global registry.
func (s *Service) latestAuthForModelRegistration(authID string) (*coreauth.Auth, bool) {
	if s == nil || s.coreManager == nil || authID == "" {
		return nil, false
	}
	auth, ok := s.coreManager.GetByID(authID)
	if !ok || auth == nil || auth.ID == "" {
		return nil, false
	}
	return auth, true
}

func configEntryForAuthIndex[T any](auth *coreauth.Auth, entries []T) *T {
	if auth == nil || auth.AuthSourceKind() != coreauth.AuthSourceConfig || auth.Attributes == nil {
		return nil
	}
	index, errIndex := strconv.Atoi(strings.TrimSpace(auth.Attributes[coreauth.AttributeConfigIndex]))
	if errIndex != nil || index < 0 || index >= len(entries) {
		return nil
	}
	return &entries[index]
}

func (s *Service) resolveConfigClaudeKey(auth *coreauth.Auth) *config.ClaudeKey {
	if auth == nil || s.cfg == nil {
		return nil
	}
	if entry := configEntryForAuthIndex(auth, s.cfg.ClaudeKey); entry != nil {
		return entry
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range s.cfg.ClaudeKey {
		entry := &s.cfg.ClaudeKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range s.cfg.ClaudeKey {
			entry := &s.cfg.ClaudeKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func (s *Service) resolveConfigGeminiKey(auth *coreauth.Auth) *config.GeminiKey {
	if s == nil || s.cfg == nil {
		return nil
	}
	return s.resolveConfigGeminiKeyEntry(auth, s.cfg.GeminiKey)
}

func (s *Service) resolveConfigInteractionsKey(auth *coreauth.Auth) *config.GeminiKey {
	if s == nil || s.cfg == nil {
		return nil
	}
	return s.resolveConfigGeminiKeyEntry(auth, s.cfg.InteractionsKey)
}

func (s *Service) resolveConfigGeminiKeyEntry(auth *coreauth.Auth, entries []config.GeminiKey) *config.GeminiKey {
	if auth == nil || s.cfg == nil {
		return nil
	}
	if entry := configEntryForAuthIndex(auth, entries); entry != nil {
		return entry
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range entries {
		entry := &entries[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	return nil
}

func (s *Service) resolveConfigVertexCompatKey(auth *coreauth.Auth) *config.VertexCompatKey {
	if auth == nil || s.cfg == nil {
		return nil
	}
	if entry := configEntryForAuthIndex(auth, s.cfg.VertexCompatAPIKey); entry != nil {
		return entry
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range s.cfg.VertexCompatAPIKey {
		entry := &s.cfg.VertexCompatAPIKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range s.cfg.VertexCompatAPIKey {
			entry := &s.cfg.VertexCompatAPIKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func (s *Service) resolveConfigCodexKey(auth *coreauth.Auth) *config.CodexKey {
	if s == nil || s.cfg == nil {
		return nil
	}
	return resolveConfigCodexStyleKey(auth, s.cfg.CodexKey, true)
}

func (s *Service) resolveConfigXAIKey(auth *coreauth.Auth) *config.XAIKey {
	if s == nil || s.cfg == nil {
		return nil
	}
	return resolveConfigCodexStyleKey(auth, s.cfg.XAIKey, false)
}

func (s *Service) resolveConfigOpenCodeGoKey(auth *coreauth.Auth) *config.OpenCodeGoKey {
	if auth == nil || s.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range s.cfg.OpenCodeGoKey {
		entry := &s.cfg.OpenCodeGoKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	return nil
}

func (s *Service) resolveConfigBedrockKey(auth *coreauth.Auth) *config.BedrockKey {
	if auth == nil || s.cfg == nil {
		return nil
	}
	var attrAPIKey, attrAccessKey, attrRegion, attrBase string
	if auth.Attributes != nil {
		attrAPIKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrAccessKey = strings.TrimSpace(auth.Attributes["access_key_id"])
		attrRegion = strings.TrimSpace(auth.Attributes["region"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range s.cfg.BedrockKey {
		entry := &s.cfg.BedrockKey[i]
		cfgAPIKey := strings.TrimSpace(entry.APIKey)
		cfgAccessKey := strings.TrimSpace(entry.AccessKeyID)
		cfgRegion := strings.TrimSpace(entry.Region)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if cfgAPIKey != "" && attrAPIKey != "" && strings.EqualFold(cfgAPIKey, attrAPIKey) && matchBedrockAttrs(cfgRegion, attrRegion, cfgBase, attrBase) {
			return entry
		}
		if cfgAccessKey != "" && attrAccessKey != "" && strings.EqualFold(cfgAccessKey, attrAccessKey) && matchBedrockAttrs(cfgRegion, attrRegion, cfgBase, attrBase) {
			return entry
		}
	}
	return nil
}

func matchBedrockAttrs(cfgRegion, attrRegion, cfgBase, attrBase string) bool {
	if cfgRegion != "" && attrRegion != "" && !strings.EqualFold(cfgRegion, attrRegion) {
		return false
	}
	return cfgBase == "" || attrBase == "" || strings.EqualFold(cfgBase, attrBase)
}

func resolveConfigCodexStyleKey(auth *coreauth.Auth, entries []config.CodexKey, validateIndexCredentials bool) *config.CodexKey {
	if auth == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	matchesCredentials := func(entry *config.CodexKey) bool {
		if entry == nil {
			return false
		}
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" {
			return strings.EqualFold(cfgKey, attrKey) && (cfgBase == "" || strings.EqualFold(cfgBase, attrBase))
		}
		return attrBase != "" && strings.EqualFold(cfgBase, attrBase)
	}
	if entry := configEntryForAuthIndex(auth, entries); entry != nil && (!validateIndexCredentials || matchesCredentials(entry)) {
		return entry
	}
	for i := range entries {
		if entry := &entries[i]; matchesCredentials(entry) {
			return entry
		}
	}
	return nil
}

func (s *Service) oauthExcludedModels(provider, authKind string) []string {
	cfg := s.cfg
	if cfg == nil {
		return nil
	}
	authKindKey := strings.ToLower(strings.TrimSpace(authKind))
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	if authKindKey == "apikey" {
		return nil
	}
	return cfg.OAuthExcludedModels[providerKey]
}

func applyExcludedModels(models []*ModelInfo, excluded []string) []*ModelInfo {
	if len(models) == 0 || len(excluded) == 0 {
		return models
	}

	patterns := make([]string, 0, len(excluded))
	for _, item := range excluded {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			patterns = append(patterns, strings.ToLower(trimmed))
		}
	}
	if len(patterns) == 0 {
		return models
	}

	filtered := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.ToLower(strings.TrimSpace(model.ID))
		blocked := false
		for _, pattern := range patterns {
			if matchWildcard(pattern, modelID) {
				blocked = true
				break
			}
		}
		if !blocked {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func applyModelPrefixes(models []*ModelInfo, prefix string, forceModelPrefix bool) []*ModelInfo {
	trimmedPrefix := strings.TrimSpace(prefix)
	if trimmedPrefix == "" || len(models) == 0 {
		return models
	}

	out := make([]*ModelInfo, 0, len(models)*2)
	seen := make(map[string]struct{}, len(models)*2)

	addModel := func(model *ModelInfo) {
		if model == nil {
			return
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		out = append(out, model)
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		baseID := strings.TrimSpace(model.ID)
		if baseID == "" {
			continue
		}
		if !forceModelPrefix || trimmedPrefix == baseID {
			addModel(model)
		}
		clone := *model
		clone.ID = trimmedPrefix + "/" + baseID
		addModel(&clone)
	}
	return out
}

// matchWildcard performs case-insensitive wildcard matching where '*' matches any substring.
func matchWildcard(pattern, value string) bool {
	if pattern == "" {
		return false
	}

	// Fast path for exact match (no wildcard present).
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}

	parts := strings.Split(pattern, "*")
	// Handle prefix.
	if prefix := parts[0]; prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return false
		}
		value = value[len(prefix):]
	}

	// Handle suffix.
	if suffix := parts[len(parts)-1]; suffix != "" {
		if !strings.HasSuffix(value, suffix) {
			return false
		}
		value = value[:len(value)-len(suffix)]
	}

	// Handle middle segments in order.
	for i := 1; i < len(parts)-1; i++ {
		segment := parts[i]
		if segment == "" {
			continue
		}
		idx := strings.Index(value, segment)
		if idx < 0 {
			return false
		}
		value = value[idx+len(segment):]
	}

	return true
}

type modelEntry interface {
	GetName() string
	GetAlias() string
	GetDisplayName() string
	GetThinking() *registry.ThinkingSupport
}

type modelMaxContextLengthEntry interface {
	GetMaxContextLength() int
}

type modelCompatEntry interface {
	GetIsCompat() bool
}

func buildConfiguredModelInfo(model modelEntry, ownedBy, modelType string, created int64, fallbackDisplayName string, userDefined bool) *ModelInfo {
	name := strings.TrimSpace(model.GetName())
	alias := strings.TrimSpace(model.GetAlias())
	if alias == "" {
		alias = name
	}
	if alias == "" {
		return nil
	}
	displayName := strings.TrimSpace(model.GetDisplayName())
	if displayName == "" {
		displayName = fallbackDisplayName
	}
	if displayName == "" {
		displayName = alias
	}
	info := &ModelInfo{
		ID:          alias,
		Object:      "model",
		Created:     created,
		OwnedBy:     ownedBy,
		Type:        modelType,
		DisplayName: displayName,
		UserDefined: userDefined,
	}
	if maxContextModel, okMaxContext := any(model).(modelMaxContextLengthEntry); okMaxContext {
		if maxContextLength := maxContextModel.GetMaxContextLength(); maxContextLength > 0 {
			info.ContextLength = maxContextLength
			info.MaxContextLength = maxContextLength
		}
	}
	if compatModel, okCompat := any(model).(modelCompatEntry); okCompat {
		info.IsCompat = compatModel.GetIsCompat()
	}
	return info
}

func buildOpenAICompatibilityConfigModels(compat *config.OpenAICompatibility) []*ModelInfo {
	if compat == nil || len(compat.Models) == 0 {
		return nil
	}
	now := time.Now().Unix()
	models := make([]*ModelInfo, 0, len(compat.Models))
	for i := range compat.Models {
		model := compat.Models[i]
		modelType := "openai-compatibility"
		if model.Image {
			modelType = registry.OpenAIImageModelType
		} else if model.Video {
			modelType = registry.OpenAIVideoModelType
		}
		info := buildConfiguredModelInfo(model, compat.Name, modelType, now, strings.TrimSpace(model.Alias), false)
		if info == nil {
			continue
		}
		thinkingSupport := model.Thinking
		if thinkingSupport == nil && !model.Image && !model.Video {
			thinkingSupport = &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}
		}
		info.Thinking = modelconfig.NormalizeThinkingSupport(thinkingSupport)
		info.SupportedInputModalities = normalizeCompatConfigModalities(model.InputModalities)
		info.SupportedOutputModalities = normalizeCompatConfigModalities(model.OutputModalities)
		info.Priority = model.Priority
		models = append(models, info)
	}
	return models
}

func openAICompatibilityHasEnabledAPIKey(entry *config.OpenAICompatibility) bool {
	if entry == nil {
		return false
	}
	for i := range entry.APIKeyEntries {
		if !entry.APIKeyEntries[i].Disabled && strings.TrimSpace(entry.APIKeyEntries[i].APIKey) != "" {
			return true
		}
	}
	return false
}

func (s *Service) registerDedicatedOpenAICompatibilityMetadataModels(auth *coreauth.Auth, provider string) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	if _, ok := auth.Metadata["models"]; !ok {
		return false
	}
	models := buildOpenAICompatibilityAuthMetadataModels(auth, provider)
	if len(models) == 0 {
		GlobalModelRegistry().UnregisterClient(auth.ID)
		return true
	}
	forceModelPrefix := s != nil && s.cfg != nil && s.cfg.ForceModelPrefix
	s.registerResolvedModelsForAuth(auth, provider, applyModelPrefixes(models, auth.Prefix, forceModelPrefix))
	return true
}

func buildOpenAICompatibilityAuthMetadataModels(auth *coreauth.Auth, compatName string) []*ModelInfo {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	rawModels, ok := auth.Metadata["models"]
	if !ok {
		return nil
	}
	models := normalizeOpenAICompatibilityMetadataModels(rawModels)
	if len(models) == 0 {
		return nil
	}
	compatName = strings.TrimSpace(compatName)
	if compatName == "" && auth.Attributes != nil {
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	if compatName == "" {
		compatName = strings.TrimSpace(auth.Label)
	}
	return buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{Name: compatName, Models: models})
}

func normalizeOpenAICompatibilityMetadataModels(raw any) []config.OpenAICompatibilityModel {
	switch typed := raw.(type) {
	case []config.OpenAICompatibilityModel:
		return compactOpenAICompatibilityMetadataModels(typed)
	case []string:
		models := make([]config.OpenAICompatibilityModel, 0, len(typed))
		for _, model := range typed {
			model = strings.TrimSpace(model)
			if model != "" {
				models = append(models, config.OpenAICompatibilityModel{Name: model, Alias: model})
			}
		}
		return models
	case []any:
		models := make([]config.OpenAICompatibilityModel, 0, len(typed))
		for _, item := range typed {
			switch value := item.(type) {
			case string:
				model := strings.TrimSpace(value)
				if model != "" {
					models = append(models, config.OpenAICompatibilityModel{Name: model, Alias: model})
				}
			default:
				encoded, err := json.Marshal(value)
				if err != nil {
					continue
				}
				var model config.OpenAICompatibilityModel
				if json.Unmarshal(encoded, &model) == nil {
					models = append(models, model)
				}
			}
		}
		return compactOpenAICompatibilityMetadataModels(models)
	default:
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil
		}
		var models []config.OpenAICompatibilityModel
		if json.Unmarshal(encoded, &models) == nil {
			return compactOpenAICompatibilityMetadataModels(models)
		}
		var ids []string
		if json.Unmarshal(encoded, &ids) != nil {
			return nil
		}
		return normalizeOpenAICompatibilityMetadataModels(ids)
	}
}

func compactOpenAICompatibilityMetadataModels(models []config.OpenAICompatibilityModel) []config.OpenAICompatibilityModel {
	out := make([]config.OpenAICompatibilityModel, 0, len(models))
	for _, model := range models {
		model.Name = strings.TrimSpace(model.Name)
		model.Alias = strings.TrimSpace(model.Alias)
		if model.Name == "" {
			model.Name = model.Alias
		}
		if model.Alias == "" {
			model.Alias = model.Name
		}
		if model.Name != "" {
			out = append(out, model)
		}
	}
	return out
}

func buildOpenCodeGoConfigModels(entry *config.OpenCodeGoKey) []*ModelInfo {
	if entry == nil || len(entry.Models) == 0 {
		return nil
	}
	return buildSimpleConfigModels(entry.Models, "opencode", "opencode-go")
}

func buildBedrockConfigModels(entry *config.BedrockKey) []*ModelInfo {
	if entry == nil || len(entry.Models) == 0 {
		return nil
	}
	return buildSimpleConfigModels(entry.Models, "anthropic", "bedrock")
}

type simpleModelEntry interface {
	GetName() string
	GetAlias() string
	GetDisplayName() string
}

func buildSimpleConfigModels[T simpleModelEntry](models []T, ownedBy, modelType string) []*ModelInfo {
	now := time.Now().Unix()
	out := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for i := range models {
		name := strings.TrimSpace(models[i].GetName())
		alias := strings.TrimSpace(models[i].GetAlias())
		if alias == "" {
			alias = name
		}
		if alias == "" {
			continue
		}
		key := strings.ToLower(alias)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		displayName := strings.TrimSpace(models[i].GetDisplayName())
		if displayName == "" {
			displayName = name
		}
		info := &ModelInfo{ID: alias, Object: "model", Created: now, OwnedBy: ownedBy, Type: modelType, DisplayName: displayName, UserDefined: true}
		if resolved := modelconfig.ResolveModelInfo(name, modelType, nil); resolved.Thinking != nil {
			info.Thinking = resolved.Thinking
		}
		out = append(out, info)
	}
	return out
}

func normalizeCompatConfigModalities(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		modality := strings.ToLower(strings.TrimSpace(item))
		if modality == "" {
			continue
		}
		if _, exists := seen[modality]; exists {
			continue
		}
		seen[modality] = struct{}{}
		out = append(out, modality)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildConfigModels[T modelEntry](models []T, ownedBy, modelType string) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	now := time.Now().Unix()
	out := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for i := range models {
		model := models[i]
		name := strings.TrimSpace(model.GetName())
		info := buildConfiguredModelInfo(model, ownedBy, modelType, now, name, true)
		if info == nil {
			continue
		}
		alias := info.ID
		key := strings.ToLower(alias)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if resolved := modelconfig.ResolveModelInfo(name, modelType, model.GetThinking()); resolved.Thinking != nil {
			info.Thinking = resolved.Thinking
		}
		out = append(out, info)
	}
	return out
}

func buildVertexCompatConfigModels(entry *config.VertexCompatKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	return buildConfigModels(entry.Models, "google", "vertex")
}

func buildGeminiConfigModels(entry *config.GeminiKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	return buildConfigModels(entry.Models, "google", "gemini")
}

func buildClaudeConfigModels(entry *config.ClaudeKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	return buildConfigModels(entry.Models, "anthropic", "claude")
}

func buildXAIConfigModels(entry *config.XAIKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	return buildConfigModels(entry.Models, "xai", "xai")
}

func buildCodexConfigModels(entry *config.CodexKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	if len(entry.Models) == 0 {
		return registry.GetCodexProModels()
	}

	models := buildConfigModels(entry.Models, "openai", "openai")
	configuredDisplayNames := make(map[string]string, len(entry.Models))
	seenConfiguredModels := make(map[string]struct{}, len(entry.Models))
	for i := range entry.Models {
		model := entry.Models[i]
		alias := strings.TrimSpace(model.Alias)
		if alias == "" {
			alias = strings.TrimSpace(model.Name)
		}
		if alias == "" {
			continue
		}
		key := strings.ToLower(alias)
		if _, exists := seenConfiguredModels[key]; exists {
			continue
		}
		seenConfiguredModels[key] = struct{}{}

		displayName := strings.TrimSpace(model.DisplayName)
		if displayName != "" {
			configuredDisplayNames[key] = displayName
		}
	}
	for _, model := range models {
		if model == nil {
			continue
		}
		if displayName, ok := configuredDisplayNames[strings.ToLower(model.ID)]; ok {
			model.DisplayName = displayName
		}
	}
	return models
}

func rewriteModelInfoName(name, oldID, newID string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return name
	}
	oldID = strings.TrimSpace(oldID)
	newID = strings.TrimSpace(newID)
	if oldID == "" || newID == "" {
		return name
	}
	if strings.EqualFold(oldID, newID) {
		return name
	}
	if strings.EqualFold(trimmed, oldID) {
		return newID
	}
	if strings.HasSuffix(trimmed, "/"+oldID) {
		prefix := strings.TrimSuffix(trimmed, oldID)
		return prefix + newID
	}
	if trimmed == "models/"+oldID {
		return "models/" + newID
	}
	return name
}

func applyOAuthModelAlias(cfg *config.Config, provider, authKind string, models []*ModelInfo) []*ModelInfo {
	return applyOAuthModelAliasForAuth(cfg, provider, authKind, nil, models)
}

func applyOAuthModelAliasForAuth(cfg *config.Config, provider, authKind string, attributes map[string]string, models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return models
	}
	channel := coreauth.OAuthModelAliasChannel(provider, authKind)
	if channel == "" {
		return models
	}
	aliases := oauthModelAliasesForAuth(cfg, channel, attributes)
	for _, defaultAlias := range coreauth.OAuthModelAliasDefaults(channel) {
		for _, model := range models {
			if model != nil && strings.EqualFold(strings.TrimSpace(model.ID), strings.TrimSpace(defaultAlias.Name)) {
				aliases = append(aliases, defaultAlias)
				break
			}
		}
	}
	if len(aliases) == 0 {
		return models
	}
	return applyOAuthModelAliasEntries(aliases, models)
}

func oauthModelAliasesForAuth(cfg *config.Config, channel string, attributes map[string]string) []config.OAuthModelAlias {
	perAuthAliases := coreauth.OAuthModelAliasesFromAttributes(attributes)
	var globalAliases []config.OAuthModelAlias
	if cfg != nil {
		globalAliases = cfg.OAuthModelAlias[channel]
	}
	if len(perAuthAliases) == 0 && len(globalAliases) == 0 {
		return nil
	}
	if len(globalAliases) == 0 {
		return perAuthAliases
	}
	out := make([]config.OAuthModelAlias, 0, len(perAuthAliases)+len(globalAliases))
	seenAlias := make(map[string]struct{}, len(perAuthAliases)+len(globalAliases))
	add := func(aliases []config.OAuthModelAlias) {
		for _, entry := range aliases {
			alias := strings.TrimSpace(entry.Alias)
			if alias == "" {
				continue
			}
			key := strings.ToLower(alias)
			if _, exists := seenAlias[key]; exists {
				continue
			}
			seenAlias[key] = struct{}{}
			out = append(out, entry)
		}
	}
	add(perAuthAliases)
	add(globalAliases)
	return out
}

func applyOAuthModelAliasEntries(aliases []config.OAuthModelAlias, models []*ModelInfo) []*ModelInfo {
	type aliasEntry struct {
		alias       string
		displayName string
		fork        bool
	}

	forward := make(map[string][]aliasEntry, len(aliases))
	for i := range aliases {
		name := strings.TrimSpace(aliases[i].Name)
		alias := strings.TrimSpace(aliases[i].Alias)
		if name == "" || alias == "" {
			continue
		}
		if strings.EqualFold(name, alias) {
			continue
		}
		key := strings.ToLower(name)
		forward[key] = append(forward[key], aliasEntry{
			alias:       alias,
			displayName: strings.TrimSpace(aliases[i].DisplayName),
			fork:        aliases[i].Fork,
		})
	}
	if len(forward) == 0 {
		return models
	}

	out := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	matchedSources := make(map[string]struct{}, len(forward))
	for _, model := range models {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		entries := forward[key]
		if len(entries) == 0 {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, model)
			continue
		}
		matchedSources[key] = struct{}{}

		keepOriginal := false
		for _, entry := range entries {
			if entry.fork {
				keepOriginal = true
				break
			}
		}
		if keepOriginal {
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				out = append(out, model)
			}
		}

		addedAlias := false
		for _, entry := range entries {
			mappedID := strings.TrimSpace(entry.alias)
			if mappedID == "" {
				continue
			}
			if strings.EqualFold(mappedID, id) {
				continue
			}
			aliasKey := strings.ToLower(mappedID)
			if _, exists := seen[aliasKey]; exists {
				continue
			}
			seen[aliasKey] = struct{}{}
			clone := *model
			clone.ID = mappedID
			if entry.displayName != "" {
				clone.DisplayName = entry.displayName
			}
			if clone.Name != "" {
				clone.Name = rewriteModelInfoName(clone.Name, id, mappedID)
			}
			out = append(out, &clone)
			addedAlias = true
		}

		if !keepOriginal && !addedAlias {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, model)
		}
	}
	for sourceKey, entries := range forward {
		if _, matched := matchedSources[sourceKey]; matched {
			continue
		}
		for _, entry := range entries {
			mappedID := strings.TrimSpace(entry.alias)
			if mappedID == "" {
				continue
			}
			aliasKey := strings.ToLower(mappedID)
			if _, exists := seen[aliasKey]; exists {
				continue
			}
			seen[aliasKey] = struct{}{}
			model := &ModelInfo{ID: mappedID, Object: "model", Name: "models/" + mappedID, Version: mappedID}
			if entry.displayName != "" {
				model.DisplayName = entry.displayName
			}
			out = append(out, model)
		}
	}
	return out
}

func applyModelOverrides(cfg *config.Config, provider, authKind string, models []*ModelInfo) []*ModelInfo {
	if cfg == nil || len(cfg.ModelOverrides) == 0 || len(models) == 0 {
		return models
	}
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	channel := coreauth.OAuthModelAliasChannel(providerKey, authKind)
	if channel == "" {
		channel = providerKey
	}

	out := make([]*ModelInfo, 0, len(models))
	changed := false
	for _, model := range models {
		if model == nil {
			continue
		}
		applied := false
		clone := *model
		for i := range cfg.ModelOverrides {
			override := cfg.ModelOverrides[i]
			if !modelOverrideMatches(override, providerKey, channel, model) {
				continue
			}
			if override.ContextLength > 0 {
				clone.ContextLength = override.ContextLength
			}
			if override.MaxCompletionTokens > 0 {
				clone.MaxCompletionTokens = override.MaxCompletionTokens
			}
			if override.Priority > 0 {
				clone.Priority = override.Priority
			}
			applied = true
		}
		if applied {
			modelCopy := clone
			out = append(out, &modelCopy)
			changed = true
			continue
		}
		out = append(out, model)
	}
	if !changed {
		return models
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := 0
		right := 0
		if out[i] != nil {
			left = out[i].Priority
		}
		if out[j] != nil {
			right = out[j].Priority
		}
		return left > right
	})
	return out
}

func modelOverrideMatches(override config.ModelOverride, provider, channel string, model *ModelInfo) bool {
	if model == nil {
		return false
	}
	if override.Channel != "" && !strings.EqualFold(override.Channel, channel) {
		return false
	}
	if override.Provider != "" && !strings.EqualFold(override.Provider, provider) {
		return false
	}
	target := strings.TrimSpace(override.Model)
	if target == "" {
		return false
	}
	return modelIDMatchesOverride(model.ID, target) ||
		modelIDMatchesOverride(model.DisplayName, target) ||
		modelIDMatchesOverride(model.Name, target)
}

func modelIDMatchesOverride(value, target string) bool {
	value = strings.TrimSpace(value)
	target = strings.TrimSpace(target)
	if value == "" || target == "" {
		return false
	}
	if strings.EqualFold(value, target) {
		return true
	}
	return strings.HasPrefix(value, "models/") && strings.EqualFold(strings.TrimPrefix(value, "models/"), target)
}
