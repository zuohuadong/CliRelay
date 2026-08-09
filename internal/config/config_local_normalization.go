package config

import (
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func (cfg *Config) OpenAICompatibilityAliasProviders(modelName string) []string {
	if cfg == nil {
		return nil
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil
	}
	providers := make([]string, 0, 4)
	seen := make(map[string]struct{})
	appendEntries := func(defaultProvider string, entries []OpenAICompatibility, genericOpenAICompat bool) {
		for i := range entries {
			entry := entries[i]
			if entry.Disabled || !openAICompatibilityModelsSupportName(entry.Models, modelName) {
				continue
			}
			provider := strings.TrimSpace(entry.Name)
			if provider == "" {
				provider = defaultProvider
			}
			provider = strings.TrimSpace(strings.ToLower(provider))
			if genericOpenAICompat {
				provider = openAICompatibleProviderKey(provider)
			}
			if provider == "" {
				continue
			}
			if _, ok := seen[provider]; ok {
				continue
			}
			seen[provider] = struct{}{}
			providers = append(providers, provider)
		}
	}
	appendEntries(DefaultBigModelCodingProviderName, cfg.BigModelCodingAPIKey, false)
	appendEntries(DefaultAstronCodeProviderName, cfg.AstronCodeAPIKey, false)
	appendEntries(DefaultAgnesProviderName, cfg.AgnesAPIKey, false)
	appendEntries("", cfg.OpenAICompatibility, true)
	return providers
}

func (cfg *Config) OAuthModelAliasProviders(modelName string) []string {
	if cfg == nil || len(cfg.OAuthModelAlias) == 0 {
		return nil
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil
	}

	providers := make([]string, 0, len(cfg.OAuthModelAlias))
	seen := make(map[string]struct{}, len(cfg.OAuthModelAlias))
	channels := make([]string, 0, len(cfg.OAuthModelAlias))
	for channel := range cfg.OAuthModelAlias {
		channels = append(channels, channel)
	}
	sort.Strings(channels)

	for _, channel := range channels {
		aliases := cfg.OAuthModelAlias[channel]
		provider := strings.ToLower(strings.TrimSpace(channel))
		if provider == "" {
			continue
		}
		for _, entry := range aliases {
			if !strings.EqualFold(strings.TrimSpace(entry.Alias), modelName) {
				continue
			}
			if _, exists := seen[provider]; exists {
				break
			}
			seen[provider] = struct{}{}
			providers = append(providers, provider)
			break
		}
	}
	return providers
}

func openAICompatibleProviderKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "openai-compatibility" || strings.HasPrefix(name, "openai-compatible-") {
		if name == "" {
			return "openai-compatibility"
		}
		return name
	}
	return "openai-compatible-" + name
}

func openAICompatibilityModelsSupportName(models []OpenAICompatibilityModel, modelName string) bool {
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.Alias), modelName) || strings.EqualFold(strings.TrimSpace(model.Name), modelName) {
			return true
		}
	}
	return false
}

func applyResponsesMemoryDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	cfg.ResponsesMaxInboundBytes = DefaultResponsesMaxInboundBytes
	cfg.ResponsesMemoryBudgetBytes = DefaultResponsesMemoryBudgetBytes
	cfg.ResponsesWebsocketMaxSessionBytes = DefaultResponsesWebsocketMaxSessionBytes
	cfg.ResponsesWebsocketMaxTurnOutputBytes = DefaultResponsesWebsocketMaxTurnOutputBytes
	cfg.ResponsesWebsocketToolCacheBytes = DefaultResponsesWebsocketToolCacheBytes
	cfg.ResponsesWebsocketMemoryBudgetBytes = DefaultResponsesWebsocketMemoryBudgetBytes
	cfg.ResponsesWebsocketMaxConnections = DefaultResponsesWebsocketMaxConnections
}

// SanitizeRequestPolicies normalizes request policy matching and drops inactive rules.

func (cfg *Config) SanitizeRequestPolicies() {
	if cfg == nil || len(cfg.RequestPolicies) == 0 {
		return
	}
	out := make([]RequestPolicy, 0, len(cfg.RequestPolicies))
	for i := range cfg.RequestPolicies {
		policy := cfg.RequestPolicies[i]
		policy.Name = strings.TrimSpace(policy.Name)
		policy.Match.RequestedModels = normalizePolicyValues(policy.Match.RequestedModels, false)
		policy.Match.UpstreamProviders = normalizePolicyValues(policy.Match.UpstreamProviders, true)
		policy.Match.UpstreamModels = normalizePolicyValues(policy.Match.UpstreamModels, false)
		policy.Match.RequestFeatures = normalizePolicyValues(policy.Match.RequestFeatures, true)
		policy.OverLimit.Action = strings.ToLower(strings.TrimSpace(policy.OverLimit.Action))
		switch policy.OverLimit.Action {
		case "", "skip-channel", "reject", "compress":
		default:
			policy.OverLimit.Action = "skip-channel"
		}
		policy.OverLimit.Compression.Provider = strings.ToLower(strings.TrimSpace(policy.OverLimit.Compression.Provider))
		policy.OverLimit.Compression.Model = strings.TrimSpace(policy.OverLimit.Compression.Model)
		policy.OverLimit.Compression.UnavailableAction = strings.ToLower(strings.TrimSpace(policy.OverLimit.Compression.UnavailableAction))
		switch policy.OverLimit.Compression.UnavailableAction {
		case "skip", "reject":
		case "":
			policy.OverLimit.Compression.UnavailableAction = "reject"
		default:
			policy.OverLimit.Compression.UnavailableAction = "reject"
		}
		if policy.OverLimit.Action == "compress" {
			compression := &policy.OverLimit.Compression
			if compression.TriggerRatio <= 0 || compression.TriggerRatio >= 1 {
				compression.TriggerRatio = 0.82
			}
			if compression.TargetRatio <= 0 || compression.TargetRatio >= compression.TriggerRatio {
				compression.TargetRatio = 0.60
			}
			if compression.SafetyMarginPercent <= 0 || compression.SafetyMarginPercent >= 50 {
				compression.SafetyMarginPercent = 10
			}
			if compression.PreserveRecentItems <= 0 {
				compression.PreserveRecentItems = 8
			}
			if compression.CacheTTLSeconds <= 0 {
				compression.CacheTTLSeconds = 3600
			}
			if compression.CacheMaxEntries <= 0 {
				compression.CacheMaxEntries = 4096
			}
			compression.MediaMode = strings.ToLower(strings.TrimSpace(compression.MediaMode))
			switch compression.MediaMode {
			case "", "auto":
				compression.MediaMode = "auto"
			case "preserve":
			default:
				compression.MediaMode = "preserve"
			}
			if policy.OverLimit.Compression.Provider == "" || policy.OverLimit.Compression.Model == "" {
				policy.OverLimit.Action = "skip-channel"
			}
		}
		autoContext := policy.OverLimit.Action == "compress" && (policy.OverLimit.Compression.AutoContext == nil || *policy.OverLimit.Compression.AutoContext)
		if policy.Limits.MaxRequestBytes <= 0 && policy.Limits.MinRequestBytes <= 0 && policy.Limits.MaxInputTokens <= 0 && policy.Limits.MinInputTokens <= 0 && policy.Limits.MinInputItems <= 0 && policy.Limits.MinToolCalls <= 0 && len(policy.Match.RequestFeatures) == 0 && !autoContext {
			continue
		}
		out = append(out, policy)
	}
	cfg.RequestPolicies = out
}

// SanitizeProviderPreferences normalizes model-scoped upstream provider priority overrides.

func (cfg *Config) SanitizeProviderPreferences() {
	if cfg == nil || len(cfg.ProviderPreferences) == 0 {
		return
	}
	out := make([]ProviderPreference, 0, len(cfg.ProviderPreferences))
	for i := range cfg.ProviderPreferences {
		rule := cfg.ProviderPreferences[i]
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Match.RequestedModels = normalizePolicyValues(rule.Match.RequestedModels, false)
		rule.Match.UpstreamProviders = normalizePolicyValues(rule.Match.UpstreamProviders, true)
		rule.Match.UpstreamModels = normalizePolicyValues(rule.Match.UpstreamModels, false)
		if rule.Priority <= 0 {
			continue
		}
		if len(rule.Match.RequestedModels) == 0 && len(rule.Match.UpstreamProviders) == 0 && len(rule.Match.UpstreamModels) == 0 {
			continue
		}
		out = append(out, rule)
	}
	cfg.ProviderPreferences = out
}

// SanitizeContextRetrieval normalizes local context retrieval defaults.

func (cfg *Config) SanitizeContextRetrieval() {
	if cfg == nil {
		return
	}
	cr := &cfg.ContextRetrieval
	if !cr.Enabled {
		return
	}
	if cr.MaxInputBytes <= 0 {
		cr.MaxInputBytes = 700000
	}
	if cr.PreserveRecentTurns <= 0 {
		cr.PreserveRecentTurns = 6
	}
	if cr.Chunk.MaxBytes <= 0 {
		cr.Chunk.MaxBytes = 12000
	}
	if cr.Retrieval.TopK <= 0 {
		cr.Retrieval.TopK = 20
	}
	cr.Retrieval.Strategy = strings.ToLower(strings.TrimSpace(cr.Retrieval.Strategy))
	if cr.Retrieval.Strategy == "" {
		cr.Retrieval.Strategy = "keyword"
	}
	if cr.Retrieval.Strategy != "keyword" {
		cr.Retrieval.Strategy = "keyword"
	}
	if cr.CodexAware.Enabled {
		cr.CodexAware.ToolPairRepair = strings.ToLower(strings.TrimSpace(cr.CodexAware.ToolPairRepair))
		if cr.CodexAware.ToolPairRepair == "" && cr.CodexAware.PreserveToolPairs {
			cr.CodexAware.ToolPairRepair = "drop-orphans"
		}
		if cr.CodexAware.MaxSummaryBytes <= 0 {
			cr.CodexAware.MaxSummaryBytes = 4000
		}
		if cr.CodexAware.PreserveRecentCommands <= 0 {
			cr.CodexAware.PreserveRecentCommands = 8
		}
		if cr.CodexAware.PreserveRecentErrors <= 0 {
			cr.CodexAware.PreserveRecentErrors = 8
		}
	}
	if cr.Secondary.Enabled {
		if cr.Secondary.MaxInputBytes <= 0 || cr.Secondary.MaxInputBytes >= cr.MaxInputBytes {
			cr.Secondary.MaxInputBytes = cr.MaxInputBytes * 2 / 3
		}
		if cr.Secondary.MaxInputBytes <= 0 {
			cr.Secondary.MaxInputBytes = cr.MaxInputBytes
		}
		if cr.Secondary.PreserveRecentTurns <= 0 || cr.Secondary.PreserveRecentTurns >= cr.PreserveRecentTurns {
			cr.Secondary.PreserveRecentTurns = cr.PreserveRecentTurns / 2
		}
		if cr.Secondary.PreserveRecentTurns <= 0 {
			cr.Secondary.PreserveRecentTurns = 1
		}
		if cr.Secondary.TopK <= 0 || cr.Secondary.TopK >= cr.Retrieval.TopK {
			cr.Secondary.TopK = cr.Retrieval.TopK / 2
		}
		if cr.Secondary.TopK <= 0 {
			cr.Secondary.TopK = 8
		}
		if cr.Secondary.MaxSummaryBytes <= 0 {
			cr.Secondary.MaxSummaryBytes = cr.CodexAware.MaxSummaryBytes / 2
		}
		if cr.Secondary.MaxSummaryBytes <= 0 {
			cr.Secondary.MaxSummaryBytes = 2000
		}
		if cr.Secondary.MaxItemBytes <= 0 {
			cr.Secondary.MaxItemBytes = cr.Secondary.MaxInputBytes / 4
		}
		if cr.Secondary.MaxItemBytes <= 0 {
			cr.Secondary.MaxItemBytes = 24000
		}
	}
}

// SanitizeMultimodalAdapters normalizes multimodal adapter configuration.

func (cfg *Config) SanitizeMultimodalAdapters() {
	if cfg == nil {
		return
	}
	ma := &cfg.MultimodalAdapters
	if ma.Enabled == nil || !*ma.Enabled {
		return
	}
	ma.DefaultAction = normalizeMultimodalAdapterAction(ma.DefaultAction)
	ma.UnavailableAction = normalizeMultimodalUnavailableAction(ma.UnavailableAction)
	if strings.TrimSpace(ma.InjectAs) == "" {
		ma.InjectAs = "visual_context"
	}
	if ma.MaxMediaItems <= 0 {
		ma.MaxMediaItems = 4
	}
	if ma.MaxOutputBytes <= 0 {
		ma.MaxOutputBytes = 12000
	}
	seen := make(map[string]struct{})
	extractors := make([]MultimodalExtractorConfig, 0, len(ma.Extractors))
	for _, extractor := range ma.Extractors {
		extractor.Name = strings.TrimSpace(extractor.Name)
		if extractor.Name == "" {
			continue
		}
		key := strings.ToLower(extractor.Name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		extractors = append(extractors, extractor)
	}
	ma.Extractors = extractors

	rules := make([]MultimodalAdapterRule, 0, len(ma.Rules))
	for _, rule := range ma.Rules {
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Extractor = strings.TrimSpace(rule.Extractor)
		if rule.Extractor == "" && len(extractors) > 0 {
			rule.Extractor = extractors[0].Name
		}
		rule.Action = normalizeMultimodalAdapterAction(rule.Action)
		if strings.TrimSpace(rule.UnavailableAction) != "" {
			rule.UnavailableAction = normalizeMultimodalUnavailableAction(rule.UnavailableAction)
		}
		rule.InjectAs = strings.TrimSpace(rule.InjectAs)
		rule.Match.RequestedModels = normalizePolicyValues(rule.Match.RequestedModels, false)
		rule.Match.UpstreamProviders = normalizePolicyValues(rule.Match.UpstreamProviders, true)
		rule.Match.UpstreamModels = normalizePolicyValues(rule.Match.UpstreamModels, false)
		rule.Match.Protocols = normalizePolicyValues(rule.Match.Protocols, true)
		if len(rule.Match.RequestedModels) == 0 && len(rule.Match.UpstreamProviders) == 0 && len(rule.Match.UpstreamModels) == 0 && len(rule.Match.Protocols) == 0 {
			continue
		}
		rules = append(rules, rule)
	}
	ma.Rules = rules
}

func normalizeMultimodalAdapterAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "extract", "mcp-extract", "http-extract":
		return "extract"
	case "reject":
		return "reject"
	case "strip":
		return "strip"
	default:
		return "extract"
	}
}

func normalizeMultimodalUnavailableAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "reject":
		return "reject"
	case "strip":
		return "strip"
	case "pass-through":
		return "pass-through"
	default:
		return "strip"
	}
}

func normalizePolicyValues(values []string, lower bool) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			continue
		}
		if lower {
			trimmed = strings.ToLower(trimmed)
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// NormalizePluginsConfig applies default plugin configuration values.

func (cfg *Config) SanitizeKimiHeaderDefaults() {
	if cfg == nil {
		return
	}
	cfg.KimiHeaderDefaults.UserAgent = strings.TrimSpace(cfg.KimiHeaderDefaults.UserAgent)
	cfg.KimiHeaderDefaults.Platform = strings.TrimSpace(cfg.KimiHeaderDefaults.Platform)
	cfg.KimiHeaderDefaults.Version = strings.TrimSpace(cfg.KimiHeaderDefaults.Version)
	cfg.KimiHeaderDefaults.DeviceName = strings.TrimSpace(cfg.KimiHeaderDefaults.DeviceName)
	cfg.KimiHeaderDefaults.DeviceModel = strings.TrimSpace(cfg.KimiHeaderDefaults.DeviceModel)
}

// SanitizeOAuthModelAlias normalizes and deduplicates global OAuth model name aliases.
// It trims whitespace, normalizes channel keys to lower-case, drops empty entries,
// allows multiple aliases per upstream name, and ensures aliases are unique within each channel.

func (cfg *Config) SanitizeModelOverrides() {
	if cfg == nil || len(cfg.ModelOverrides) == 0 {
		return
	}
	out := make([]ModelOverride, 0, len(cfg.ModelOverrides))
	seen := make(map[string]struct{}, len(cfg.ModelOverrides))
	for i := range cfg.ModelOverrides {
		override := cfg.ModelOverrides[i]
		override.Channel = strings.ToLower(strings.TrimSpace(override.Channel))
		override.Provider = strings.ToLower(strings.TrimSpace(override.Provider))
		override.Model = strings.TrimSpace(override.Model)
		if override.Model == "" {
			continue
		}
		if override.Priority < 0 {
			override.Priority = 0
		}
		if override.ContextLength < 0 {
			override.ContextLength = 0
		}
		if override.MaxCompletionTokens < 0 {
			override.MaxCompletionTokens = 0
		}
		key := strings.ToLower(override.Channel + "\x00" + override.Provider + "\x00" + override.Model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, override)
	}
	cfg.ModelOverrides = out
}

// SanitizeOpenAICompatibility removes OpenAI-compatibility provider entries that are
// not actionable, specifically those missing a BaseURL. It trims whitespace before
// evaluation and preserves the relative order of remaining entries.

func (cfg *Config) MigrateBigModelCodingFromOpenAICompatibility() {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return
	}
	nextCompat := make([]OpenAICompatibility, 0, len(cfg.OpenAICompatibility))
	for i := range cfg.OpenAICompatibility {
		entry := cfg.OpenAICompatibility[i]
		if strings.EqualFold(strings.TrimSpace(entry.Name), DefaultBigModelCodingProviderName) {
			cfg.BigModelCodingAPIKey = append(cfg.BigModelCodingAPIKey, entry)
			continue
		}
		nextCompat = append(nextCompat, entry)
	}
	cfg.OpenAICompatibility = nextCompat
}

// SanitizeBigModelCoding normalizes dedicated Zhipu Coding Plan entries and
// ensures the default gpt-5.3-codex -> glm-5.1 alias remains present.

func (cfg *Config) SanitizeBigModelCoding() {
	if cfg == nil {
		return
	}
	if len(cfg.BigModelCodingAPIKeyLegacy) > 0 {
		cfg.BigModelCodingAPIKey = append(cfg.BigModelCodingAPIKey, cfg.BigModelCodingAPIKeyLegacy...)
		cfg.BigModelCodingAPIKeyLegacy = nil
	}
	if len(cfg.BigModelCodingAPIKey) == 0 {
		return
	}
	out := make([]OpenAICompatibility, 0, len(cfg.BigModelCodingAPIKey))
	for i := range cfg.BigModelCodingAPIKey {
		e := cfg.BigModelCodingAPIKey[i]
		e.Name = DefaultBigModelCodingProviderName
		e.Prefix = normalizeModelPrefix(e.Prefix)
		e.BaseURL = strings.TrimSpace(e.BaseURL)
		if e.BaseURL == "" {
			e.BaseURL = DefaultBigModelCodingBaseURL
		}
		e.TestModel = strings.TrimSpace(e.TestModel)
		if e.TestModel == "" {
			e.TestModel = DefaultBigModelCodingModel
		}
		e.Headers = NormalizeHeaders(e.Headers)
		e.IdentityFingerprint = "codex"
		e.Models = ensureBigModelCodingModels(e.Models)
		out = append(out, e)
	}
	cfg.BigModelCodingAPIKey = out
}

func ensureBigModelCodingModels(models []OpenAICompatibilityModel) []OpenAICompatibilityModel {
	for i := range models {
		models[i].Name = strings.TrimSpace(models[i].Name)
		models[i].Alias = strings.TrimSpace(models[i].Alias)
	}
	return models
}

func (cfg *Config) MigrateAstronCodeFromOpenAICompatibility() {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return
	}
	nextCompat := make([]OpenAICompatibility, 0, len(cfg.OpenAICompatibility))
	for i := range cfg.OpenAICompatibility {
		entry := cfg.OpenAICompatibility[i]
		if strings.EqualFold(strings.TrimSpace(entry.Name), DefaultAstronCodeProviderName) {
			cfg.AstronCodeAPIKey = append(cfg.AstronCodeAPIKey, entry)
			continue
		}
		nextCompat = append(nextCompat, entry)
	}
	cfg.OpenAICompatibility = nextCompat
}

func (cfg *Config) SanitizeAstronCode() {
	if cfg == nil {
		return
	}
	if len(cfg.AstronCodeAPIKey) == 0 {
		return
	}
	out := make([]OpenAICompatibility, 0, len(cfg.AstronCodeAPIKey))
	for i := range cfg.AstronCodeAPIKey {
		e := cfg.AstronCodeAPIKey[i]
		e.Name = DefaultAstronCodeProviderName
		e.Prefix = normalizeModelPrefix(e.Prefix)
		e.BaseURL = strings.TrimSpace(e.BaseURL)
		if e.BaseURL == "" {
			e.BaseURL = DefaultAstronCodeBaseURL
		}
		e.TestModel = strings.TrimSpace(e.TestModel)
		if e.TestModel == "" {
			e.TestModel = firstOpenAICompatibilityModelName(e.Models)
		}
		if e.TestModel == "" {
			e.TestModel = DefaultAstronCodeModel
		}
		e.Headers = NormalizeHeaders(e.Headers)
		e.IdentityFingerprint = "codex"
		e.ResponseEndpoint = !e.ForceChatCompletions
		e.Models = ensureAstronCodeModels(e.Models, e.ResponseEndpoint)
		out = append(out, e)
	}
	cfg.AstronCodeAPIKey = out
}

func ensureAstronCodeModels(models []OpenAICompatibilityModel, responseEndpoint bool) []OpenAICompatibilityModel {
	for i := range models {
		models[i].Name = strings.TrimSpace(models[i].Name)
		models[i].Alias = strings.TrimSpace(models[i].Alias)
	}
	return models
}

func firstOpenAICompatibilityModelName(models []OpenAICompatibilityModel) string {
	for i := range models {
		if name := strings.TrimSpace(models[i].Name); name != "" {
			return name
		}
	}
	return ""
}

type legacyConfigData struct {
	LegacyGeminiKeys      []string                    `yaml:"generative-language-api-key"`
	OpenAICompat          []legacyOpenAICompatibility `yaml:"openai-compatibility"`
	AmpUpstreamURL        string                      `yaml:"amp-upstream-url"`
	AmpUpstreamAPIKey     string                      `yaml:"amp-upstream-api-key"`
	AmpRestrictManagement *bool                       `yaml:"amp-restrict-management-to-localhost"`
	AmpModelMappings      []AmpModelMapping           `yaml:"amp-model-mappings"`
}

type legacyOpenAICompatibility struct {
	Name    string   `yaml:"name"`
	BaseURL string   `yaml:"base-url"`
	APIKeys []string `yaml:"api-keys"`
}

func (cfg *Config) migrateLegacyGeminiKeys(legacy []string) bool {
	if cfg == nil || len(legacy) == 0 {
		return false
	}
	changed := false
	seen := make(map[string]struct{}, len(cfg.GeminiKey))
	for i := range cfg.GeminiKey {
		key := strings.TrimSpace(cfg.GeminiKey[i].APIKey)
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
	}
	for _, raw := range legacy {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		cfg.GeminiKey = append(cfg.GeminiKey, GeminiKey{APIKey: key})
		seen[key] = struct{}{}
		changed = true
	}
	return changed
}

func (cfg *Config) migrateLegacyOpenAICompatibilityKeys(legacy []legacyOpenAICompatibility) bool {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 || len(legacy) == 0 {
		return false
	}
	changed := false
	for _, legacyEntry := range legacy {
		if len(legacyEntry.APIKeys) == 0 {
			continue
		}
		target := findOpenAICompatTarget(cfg.OpenAICompatibility, legacyEntry.Name, legacyEntry.BaseURL)
		if target == nil {
			continue
		}
		if mergeLegacyOpenAICompatAPIKeys(target, legacyEntry.APIKeys) {
			changed = true
		}
	}
	return changed
}

func mergeLegacyOpenAICompatAPIKeys(entry *OpenAICompatibility, keys []string) bool {
	if entry == nil || len(keys) == 0 {
		return false
	}
	changed := false
	existing := make(map[string]struct{}, len(entry.APIKeyEntries))
	for i := range entry.APIKeyEntries {
		key := strings.TrimSpace(entry.APIKeyEntries[i].APIKey)
		if key == "" {
			continue
		}
		existing[key] = struct{}{}
	}
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, ok := existing[key]; ok {
			continue
		}
		entry.APIKeyEntries = append(entry.APIKeyEntries, OpenAICompatibilityAPIKey{APIKey: key})
		existing[key] = struct{}{}
		changed = true
	}
	return changed
}

func findOpenAICompatTarget(entries []OpenAICompatibility, legacyName, legacyBase string) *OpenAICompatibility {
	nameKey := strings.ToLower(strings.TrimSpace(legacyName))
	baseKey := strings.ToLower(strings.TrimSpace(legacyBase))
	if nameKey != "" && baseKey != "" {
		for i := range entries {
			if strings.ToLower(strings.TrimSpace(entries[i].Name)) == nameKey &&
				strings.ToLower(strings.TrimSpace(entries[i].BaseURL)) == baseKey {
				return &entries[i]
			}
		}
	}
	if baseKey != "" {
		for i := range entries {
			if strings.ToLower(strings.TrimSpace(entries[i].BaseURL)) == baseKey {
				return &entries[i]
			}
		}
	}
	if nameKey != "" {
		for i := range entries {
			if strings.ToLower(strings.TrimSpace(entries[i].Name)) == nameKey {
				return &entries[i]
			}
		}
	}
	return nil
}

func (cfg *Config) migrateLegacyAmpConfig(legacy *legacyConfigData) bool {
	if cfg == nil || legacy == nil {
		return false
	}
	changed := false
	if cfg.AmpCode.UpstreamURL == "" {
		if val := strings.TrimSpace(legacy.AmpUpstreamURL); val != "" {
			cfg.AmpCode.UpstreamURL = val
			changed = true
		}
	}
	if cfg.AmpCode.UpstreamAPIKey == "" {
		if val := strings.TrimSpace(legacy.AmpUpstreamAPIKey); val != "" {
			cfg.AmpCode.UpstreamAPIKey = val
			changed = true
		}
	}
	if legacy.AmpRestrictManagement != nil {
		cfg.AmpCode.RestrictManagementToLocalhost = *legacy.AmpRestrictManagement
		changed = true
	}
	if len(cfg.AmpCode.ModelMappings) == 0 && len(legacy.AmpModelMappings) > 0 {
		cfg.AmpCode.ModelMappings = append([]AmpModelMapping(nil), legacy.AmpModelMappings...)
		changed = true
	}
	return changed
}

func removeLegacyBigModelCodingAPIKey(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	removeMapKey(root, "bigmodel-coding-api-key")
}

func removeLegacyAmpKeys(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	removeMapKey(root, "amp-upstream-url")
	removeMapKey(root, "amp-upstream-api-key")
	removeMapKey(root, "amp-restrict-management-to-localhost")
	removeMapKey(root, "amp-model-mappings")
}
