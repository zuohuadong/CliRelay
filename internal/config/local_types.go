package config

import "strings"

const (
	DefaultBigModelCodingProviderName = "bigmodel-coding"
	DefaultBigModelCodingBaseURL      = "https://open.bigmodel.cn/api/coding/paas/v4"
	DefaultBigModelCodingModel        = "glm-5.1"
	DefaultBigModelCodingAlias        = "gpt-5.3-codex"

	DefaultAstronCodeProviderName = "astron-code"
	DefaultAstronCodeBaseURL      = "https://maas-coding-api.cn-huabei-1.xf-yun.com/v2"
	DefaultAstronCodeModel        = "astron-code-latest"
	DefaultAstronCodeAlias        = "gpt-5.3-codex"
	DefaultAstronCodeGLMAlias     = DefaultBigModelCodingModel

	DefaultAgnesProviderName = "agnes"
	DefaultAgnesBaseURL      = "https://apihub.agnes-ai.com/v1"
	DefaultAgnesChatModel    = "agnes-2.0-flash"
	DefaultAgnesImageModel   = "agnes-image-2.1-flash"
	DefaultAgnesVideoModel   = "agnes-video-v2.0"
)

type MCPProxyConfig struct {
	Servers []MCPProxyServerConfig `yaml:"servers,omitempty" json:"servers,omitempty"`
}

type MCPProxyServerConfig struct {
	Name     string            `yaml:"name" json:"name"`
	Disabled bool              `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	BaseURL  string            `yaml:"base-url" json:"base-url"`
	Headers  map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

type KimiHeaderDefaults struct {
	UserAgent   string `yaml:"user-agent" json:"user-agent"`
	Platform    string `yaml:"platform" json:"platform"`
	Version     string `yaml:"version" json:"version"`
	DeviceName  string `yaml:"device-name" json:"device-name"`
	DeviceModel string `yaml:"device-model" json:"device-model"`
}

type IdentityFingerprintConfig struct {
	Codex  CodexIdentityFingerprintConfig  `yaml:"codex,omitempty" json:"codex,omitempty"`
	Claude ClaudeIdentityFingerprintConfig `yaml:"claude,omitempty" json:"claude,omitempty"`
}

type CodexIdentityFingerprintConfig struct {
	Enabled       bool              `yaml:"enabled" json:"enabled"`
	UserAgent     string            `yaml:"user-agent,omitempty" json:"user-agent,omitempty"`
	Version       string            `yaml:"version,omitempty" json:"version,omitempty"`
	Originator    string            `yaml:"originator,omitempty" json:"originator,omitempty"`
	WebsocketBeta string            `yaml:"websocket-beta,omitempty" json:"websocket-beta,omitempty"`
	SessionMode   string            `yaml:"session-mode,omitempty" json:"session-mode,omitempty"`
	SessionID     string            `yaml:"session-id,omitempty" json:"session-id,omitempty"`
	CustomHeaders map[string]string `yaml:"custom-headers,omitempty" json:"custom-headers,omitempty"`
}

type ClaudeIdentityFingerprintConfig struct {
	Enabled                 bool              `yaml:"enabled" json:"enabled"`
	CLIVersion              string            `yaml:"cli-version,omitempty" json:"cli-version,omitempty"`
	Entrypoint              string            `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
	UserAgent               string            `yaml:"user-agent,omitempty" json:"user-agent,omitempty"`
	AnthropicBeta           string            `yaml:"anthropic-beta,omitempty" json:"anthropic-beta,omitempty"`
	StainlessPackageVersion string            `yaml:"stainless-package-version,omitempty" json:"stainless-package-version,omitempty"`
	StainlessRuntimeVersion string            `yaml:"stainless-runtime-version,omitempty" json:"stainless-runtime-version,omitempty"`
	StainlessTimeout        string            `yaml:"stainless-timeout,omitempty" json:"stainless-timeout,omitempty"`
	SessionMode             string            `yaml:"session-mode,omitempty" json:"session-mode,omitempty"`
	SessionID               string            `yaml:"session-id,omitempty" json:"session-id,omitempty"`
	DeviceID                string            `yaml:"device-id,omitempty" json:"device-id,omitempty"`
	CustomHeaders           map[string]string `yaml:"custom-headers,omitempty" json:"custom-headers,omitempty"`
}

type ModelOverride struct {
	Channel             string `yaml:"channel,omitempty" json:"channel,omitempty"`
	Provider            string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model               string `yaml:"model" json:"model"`
	Priority            int    `yaml:"priority,omitempty" json:"priority,omitempty"`
	ContextLength       int    `yaml:"context-length,omitempty" json:"context-length,omitempty"`
	MaxCompletionTokens int    `yaml:"max-completion-tokens,omitempty" json:"max-completion-tokens,omitempty"`
}

type AmpCode struct {
	// UpstreamURL defines the upstream Amp control plane used for non-provider calls.
	UpstreamURL string `yaml:"upstream-url" json:"upstream-url"`

	// UpstreamAPIKey optionally overrides the Authorization header when proxying Amp upstream calls.
	UpstreamAPIKey string `yaml:"upstream-api-key" json:"upstream-api-key"`

	// UpstreamAPIKeys maps client API keys (from top-level api-keys) to upstream API keys.
	// When a request is authenticated with one of the APIKeys, the corresponding UpstreamAPIKey
	// is used for the upstream Amp request.
	UpstreamAPIKeys []AmpUpstreamAPIKeyEntry `yaml:"upstream-api-keys,omitempty" json:"upstream-api-keys,omitempty"`

	// RestrictManagementToLocalhost restricts Amp management routes (/api/user, /api/threads, etc.)
	// to only accept connections from localhost (127.0.0.1, ::1). When true, prevents drive-by
	// browser attacks and remote access to management endpoints. Default: false (API key auth is sufficient).
	RestrictManagementToLocalhost bool `yaml:"restrict-management-to-localhost" json:"restrict-management-to-localhost"`

	// ModelMappings defines model name mappings for Amp CLI requests.
	// When Amp requests a model that isn't available locally, these mappings
	// allow routing to an alternative model that IS available.
	ModelMappings []AmpModelMapping `yaml:"model-mappings" json:"model-mappings"`

	// ForceModelMappings when true, model mappings take precedence over local API keys.
	// When false (default), local API keys are used first if available.
	ForceModelMappings bool `yaml:"force-model-mappings" json:"force-model-mappings"`
}

type RequestPolicy struct {
	Name      string                 `yaml:"name,omitempty" json:"name,omitempty"`
	Match     RequestPolicyMatch     `yaml:"match,omitempty" json:"match,omitempty"`
	Limits    RequestPolicyLimits    `yaml:"limits,omitempty" json:"limits,omitempty"`
	OverLimit RequestPolicyOverLimit `yaml:"over-limit,omitempty" json:"over-limit,omitempty"`
}

type RequestPolicyMatch struct {
	RequestedModels   []string `yaml:"requested-models,omitempty" json:"requested-models,omitempty"`
	UpstreamProviders []string `yaml:"upstream-providers,omitempty" json:"upstream-providers,omitempty"`
	UpstreamModels    []string `yaml:"upstream-models,omitempty" json:"upstream-models,omitempty"`
	RequestFeatures   []string `yaml:"request-features,omitempty" json:"request-features,omitempty"`
}

type RequestPolicyLimits struct {
	MaxRequestBytes int64 `yaml:"max-request-bytes,omitempty" json:"max-request-bytes,omitempty"`
	MinRequestBytes int64 `yaml:"min-request-bytes,omitempty" json:"min-request-bytes,omitempty"`
	MaxInputTokens  int64 `yaml:"max-input-tokens,omitempty" json:"max-input-tokens,omitempty"`
	MinInputTokens  int64 `yaml:"min-input-tokens,omitempty" json:"min-input-tokens,omitempty"`
	MinInputItems   int   `yaml:"min-input-items,omitempty" json:"min-input-items,omitempty"`
	MinToolCalls    int   `yaml:"min-tool-calls,omitempty" json:"min-tool-calls,omitempty"`
}

type RequestPolicyOverLimit struct {
	// Action is "skip-channel", "reject", or "compress". Empty defaults to "skip-channel".
	Action string `yaml:"action,omitempty" json:"action,omitempty"`
	// Compression controls the LLM compressor used when Action is "compress".
	Compression RequestPolicyCompression `yaml:"compression,omitempty" json:"compression,omitempty"`
}

type RequestPolicyCompression struct {
	Provider           string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model              string `yaml:"model,omitempty" json:"model,omitempty"`
	TargetRequestBytes int64  `yaml:"target-request-bytes,omitempty" json:"target-request-bytes,omitempty"`
	TargetInputTokens  int64  `yaml:"target-input-tokens,omitempty" json:"target-input-tokens,omitempty"`
	// AutoContext derives trigger and target budgets from the selected upstream
	// model's context metadata. Nil defaults to enabled.
	AutoContext         *bool   `yaml:"auto-context,omitempty" json:"auto-context,omitempty"`
	TriggerRatio        float64 `yaml:"trigger-ratio,omitempty" json:"trigger-ratio,omitempty"`
	TargetRatio         float64 `yaml:"target-ratio,omitempty" json:"target-ratio,omitempty"`
	ReserveOutputTokens int64   `yaml:"reserve-output-tokens,omitempty" json:"reserve-output-tokens,omitempty"`
	SafetyMarginPercent int     `yaml:"safety-margin-percent,omitempty" json:"safety-margin-percent,omitempty"`
	PreserveRecentItems int     `yaml:"preserve-recent-items,omitempty" json:"preserve-recent-items,omitempty"`
	CacheTTLSeconds     int     `yaml:"cache-ttl-seconds,omitempty" json:"cache-ttl-seconds,omitempty"`
	CacheMaxEntries     int     `yaml:"cache-max-entries,omitempty" json:"cache-max-entries,omitempty"`
	// MediaMode is "auto" or "preserve". Auto lets a declared multimodal
	// compressor summarize old image items while preserving recent media.
	MediaMode string `yaml:"media-mode,omitempty" json:"media-mode,omitempty"`
	// UnavailableAction is "skip" or "reject". Empty defaults to "reject" so
	// an oversized request is not silently forwarded to the same small window.
	UnavailableAction string `yaml:"unavailable-action,omitempty" json:"unavailable-action,omitempty"`
	Prompt            string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
}

type ProviderPreference struct {
	Name     string                  `yaml:"name,omitempty" json:"name,omitempty"`
	Match    ProviderPreferenceMatch `yaml:"match,omitempty" json:"match,omitempty"`
	Priority int                     `yaml:"priority,omitempty" json:"priority,omitempty"`
}

const (
	DefaultCodexFingerprintUserAgent     = "codex-tui/0.145.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.145.0)"
	DefaultCodexFingerprintVersion       = "0.145.0"
	DefaultCodexFingerprintOriginator    = "codex-tui"
	DefaultCodexFingerprintWebsocketBeta = "responses_websockets=2026-02-06"
	DefaultCodexFingerprintSessionMode   = "per-request"

	DefaultClaudeFingerprintCLIVersion              = "2.1.88"
	DefaultClaudeFingerprintEntrypoint              = "cli"
	DefaultClaudeFingerprintAnthropicBeta           = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20,effort-2025-11-24"
	DefaultClaudeFingerprintStainlessPackageVersion = "0.74.0"
	DefaultClaudeFingerprintStainlessRuntimeVersion = "v22.13.0"
	DefaultClaudeFingerprintStainlessTimeout        = "600"
	DefaultClaudeFingerprintSessionMode             = "per-request"
)

func DefaultCodexIdentityFingerprint() CodexIdentityFingerprintConfig {
	return CodexIdentityFingerprintConfig{
		Enabled:       false,
		UserAgent:     DefaultCodexFingerprintUserAgent,
		Version:       DefaultCodexFingerprintVersion,
		Originator:    DefaultCodexFingerprintOriginator,
		WebsocketBeta: DefaultCodexFingerprintWebsocketBeta,
		SessionMode:   DefaultCodexFingerprintSessionMode,
		CustomHeaders: map[string]string{},
	}
}

func DefaultClaudeIdentityFingerprint() ClaudeIdentityFingerprintConfig {
	cliVersion := DefaultClaudeFingerprintCLIVersion
	entrypoint := DefaultClaudeFingerprintEntrypoint
	return ClaudeIdentityFingerprintConfig{
		Enabled:                 false,
		CLIVersion:              cliVersion,
		Entrypoint:              entrypoint,
		UserAgent:               BuildClaudeFingerprintUserAgent(cliVersion, entrypoint),
		AnthropicBeta:           DefaultClaudeFingerprintAnthropicBeta,
		StainlessPackageVersion: DefaultClaudeFingerprintStainlessPackageVersion,
		StainlessRuntimeVersion: DefaultClaudeFingerprintStainlessRuntimeVersion,
		StainlessTimeout:        DefaultClaudeFingerprintStainlessTimeout,
		SessionMode:             DefaultClaudeFingerprintSessionMode,
		CustomHeaders:           map[string]string{},
	}
}

type AmpModelMapping struct {
	// From is the model name that Amp CLI requests (e.g., "claude-opus-4.5").
	From string `yaml:"from" json:"from"`

	// To is the target model name to route to (e.g., "claude-sonnet-4").
	// The target model must have available providers in the registry.
	To string `yaml:"to" json:"to"`

	// Regex indicates whether the 'from' field should be interpreted as a regular
	// expression for matching model names. When true, this mapping is evaluated
	// after exact matches and in the order provided. Defaults to false (exact match).
	Regex bool `yaml:"regex,omitempty" json:"regex,omitempty"`
}

type AmpUpstreamAPIKeyEntry struct {
	// UpstreamAPIKey is the API key to use when proxying to the Amp upstream.
	UpstreamAPIKey string `yaml:"upstream-api-key" json:"upstream-api-key"`

	// APIKeys are the client API keys (from top-level api-keys) that map to this upstream key.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`
}

type ProviderPreferenceMatch struct {
	RequestedModels   []string `yaml:"requested-models,omitempty" json:"requested-models,omitempty"`
	UpstreamProviders []string `yaml:"upstream-providers,omitempty" json:"upstream-providers,omitempty"`
	UpstreamModels    []string `yaml:"upstream-models,omitempty" json:"upstream-models,omitempty"`
}

type ModelRouteRule struct {
	Name    string             `yaml:"name,omitempty" json:"name,omitempty"`
	Match   ModelRouteMatch    `yaml:"match,omitempty" json:"match,omitempty"`
	Measure ModelRouteMeasure  `yaml:"measure,omitempty" json:"measure,omitempty"`
	Routes  []ModelRouteBranch `yaml:"routes,omitempty" json:"routes,omitempty"`
}

type ModelRouteMatch struct {
	RequestedModels []string `yaml:"requested-models,omitempty" json:"requested-models,omitempty"`
}

type ModelRouteMeasure struct {
	// Source currently supports "estimated-input-tokens" and "request-bytes".
	Source string `yaml:"source,omitempty" json:"source,omitempty"`
	// OnMissing controls behavior when the selected measurement is absent.
	// Supported values: "passthrough" and "reject".
	OnMissing string `yaml:"on-missing,omitempty" json:"on-missing,omitempty"`
}

type ModelRouteBranch struct {
	MinInputTokens int64            `yaml:"min-input-tokens,omitempty" json:"min-input-tokens,omitempty"`
	MaxInputTokens int64            `yaml:"max-input-tokens,omitempty" json:"max-input-tokens,omitempty"`
	Action         string           `yaml:"action,omitempty" json:"action,omitempty"`
	Target         ModelRouteTarget `yaml:"target,omitempty" json:"target,omitempty"`
}

type ModelRouteTarget struct {
	Provider               string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model                  string `yaml:"model,omitempty" json:"model,omitempty"`
	PreserveRequestedModel bool   `yaml:"preserve-requested-model,omitempty" json:"preserve-requested-model,omitempty"`
}

const (
	DefaultKimiHeaderUserAgent   = "codex"
	DefaultKimiHeaderPlatform    = "codex"
	DefaultKimiHeaderVersion     = "1.0.0"
	DefaultKimiHeaderDeviceName  = "codex"
	DefaultKimiHeaderDeviceModel = "codex"
)

func (h KimiHeaderDefaults) WithDefaults() KimiHeaderDefaults {
	h.UserAgent = strings.TrimSpace(h.UserAgent)
	h.Platform = strings.TrimSpace(h.Platform)
	h.Version = strings.TrimSpace(h.Version)
	h.DeviceName = strings.TrimSpace(h.DeviceName)
	h.DeviceModel = strings.TrimSpace(h.DeviceModel)
	if h.UserAgent == "" {
		h.UserAgent = DefaultKimiHeaderUserAgent
	}
	if h.Platform == "" {
		h.Platform = DefaultKimiHeaderPlatform
	}
	if h.Version == "" {
		h.Version = DefaultKimiHeaderVersion
	}
	if h.DeviceName == "" {
		h.DeviceName = DefaultKimiHeaderDeviceName
	}
	if h.DeviceModel == "" {
		h.DeviceModel = DefaultKimiHeaderDeviceModel
	}
	return h
}
