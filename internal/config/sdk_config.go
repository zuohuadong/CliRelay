// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// DisableImageGeneration controls whether the built-in image_generation tool is injected/allowed.
	//
	// Supported values:
	//   - false (default): image_generation is enabled everywhere (normal behavior).
	//   - true: image_generation is disabled everywhere. The server stops injecting it, removes it from request payloads,
	//     and returns 404 for /v1/images/generations and /v1/images/edits.
	//   - "chat": disable image_generation injection for all non-images endpoints (e.g. /v1/responses, /v1/chat/completions),
	//     while keeping /v1/images/generations and /v1/images/edits enabled and preserving image_generation there.
	//   - "passthrough": do not modify the tool list on non-images endpoints — keep image_generation if the client
	//     sent it and do not inject it otherwise; on /v1/images/generations and /v1/images/edits behave like "chat".
	DisableImageGeneration DisableImageGenerationMode `yaml:"disable-image-generation" json:"disable-image-generation"`

	// GPTImage2BaseModel sets the base (mainline) model used by the legacy hosted
	// image_generation tool path when a Codex image request is not proxied directly
	// through the Image API.
	//
	// The value must start with "gpt-" (case-insensitive). If empty or invalid, the
	// default base model ("gpt-5.4-mini") is used.
	GPTImage2BaseModel string `yaml:"gpt-image-2-base-model,omitempty" json:"gpt-image-2-base-model,omitempty"`

	// VideoResultAuthCacheTTL controls how long video IDs stay pinned to the credential
	// that created them. Accepts duration strings like "30m" or "3h".
	// Empty or invalid values use the default 3h.
	VideoResultAuthCacheTTL string `yaml:"video-result-auth-cache-ttl,omitempty" json:"video-result-auth-cache-ttl,omitempty"`

	// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
	// to target prefixed credentials. When false, unprefixed model requests may use prefixed
	// credentials as well.
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

	// PassthroughHeaders controls whether upstream response headers are forwarded to downstream clients.
	// Default is false (disabled).
	PassthroughHeaders bool `yaml:"passthrough-headers" json:"passthrough-headers"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// ContextRetrieval reduces oversized conversational payloads using local retrieval.
	ContextRetrieval ContextRetrievalConfig `yaml:"context-retrieval,omitempty" json:"context-retrieval,omitempty"`

	// MultimodalAdapters turns unsupported media inputs into text context before routing to text models.
	MultimodalAdapters MultimodalAdaptersConfig `yaml:"multimodal-adapters,omitempty" json:"multimodal-adapters,omitempty"`

	// Observability controls optional high-cardinality diagnostic logs.
	Observability ObservabilityConfig `yaml:"observability,omitempty" json:"observability,omitempty"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`

	// RejectUnconfiguredModels rejects POST requests for models that have no
	// configured auth credentials backing them. When false (default), such
	// requests proceed to the normal routing path which may return a generic
	// upstream error. When true, the proxy returns 404 immediately with a
	// descriptive error message.
	RejectUnconfiguredModels bool `yaml:"reject-unconfigured-models" json:"rejectUnconfiguredModels"`

	// OpenRouterSyncEnabled enables periodic OpenRouter model metadata synchronization.
	// When enabled, model listings, pricing, and context window information are
	// fetched from the OpenRouter API for management metadata.
	OpenRouterSyncEnabled bool `yaml:"openrouter-sync-enabled" json:"openrouterSyncEnabled"`

	// OpenRouterSyncIntervalMinutes controls how often the OpenRouter model
	// catalog is refreshed. Minimum is 60 minutes. Default is 1440 (24 hours).
	OpenRouterSyncIntervalMinutes int `yaml:"openrouter-sync-interval-minutes" json:"openrouterSyncIntervalMinutes"`

	// OpenRouterAPIKey is an optional API key for authenticating with the
	// OpenRouter API. When set, it is included as a Bearer token in sync requests.
	OpenRouterAPIKey string `yaml:"openrouter-api-key" json:"openRouterApiKey"`
}

// StreamingConfig holds server streaming behavior configuration.
type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n").
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`

	// ResponsesWebsocketReplayRetries controls how many times a Responses websocket request may be replayed
	// when the upstream closes before any response output is produced. Nil uses the server default.
	// Set to 0 to disable these replay retries.
	ResponsesWebsocketReplayRetries *int `yaml:"responses-websocket-replay-retries,omitempty" json:"responses-websocket-replay-retries,omitempty"`
}

// ContextRetrievalConfig controls local SQLite FTS based context reduction.
type ContextRetrievalConfig struct {
	Enabled             bool                         `yaml:"enabled" json:"enabled"`
	Models              []PayloadModelRule           `yaml:"models,omitempty" json:"models,omitempty"`
	MaxInputBytes       int                          `yaml:"max-input-bytes,omitempty" json:"max-input-bytes,omitempty"`
	PreserveRecentTurns int                          `yaml:"preserve-recent-turns,omitempty" json:"preserve-recent-turns,omitempty"`
	Chunk               ContextRetrievalChunkConfig  `yaml:"chunk,omitempty" json:"chunk,omitempty"`
	Retrieval           ContextRetrievalSearchConfig `yaml:"retrieval,omitempty" json:"retrieval,omitempty"`
	CodexAware          CodexAwareContextConfig      `yaml:"codex-aware,omitempty" json:"codex-aware,omitempty"`
	Secondary           ContextRetrievalSecondPass   `yaml:"secondary,omitempty" json:"secondary,omitempty"`
}

type ContextRetrievalChunkConfig struct {
	MaxBytes int `yaml:"max-bytes,omitempty" json:"max-bytes,omitempty"`
}

type ContextRetrievalSearchConfig struct {
	TopK     int    `yaml:"top-k,omitempty" json:"top-k,omitempty"`
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
}

type ContextRetrievalSecondPass struct {
	Enabled             bool `yaml:"enabled" json:"enabled"`
	MaxInputBytes       int  `yaml:"max-input-bytes,omitempty" json:"max-input-bytes,omitempty"`
	PreserveRecentTurns int  `yaml:"preserve-recent-turns,omitempty" json:"preserve-recent-turns,omitempty"`
	TopK                int  `yaml:"top-k,omitempty" json:"top-k,omitempty"`
	MaxSummaryBytes     int  `yaml:"max-summary-bytes,omitempty" json:"max-summary-bytes,omitempty"`
	MaxItemBytes        int  `yaml:"max-item-bytes,omitempty" json:"max-item-bytes,omitempty"`
}

type CodexAwareContextConfig struct {
	Enabled                bool   `yaml:"enabled" json:"enabled"`
	PreserveToolPairs      bool   `yaml:"preserve-tool-pairs,omitempty" json:"preserve-tool-pairs,omitempty"`
	ToolPairRepair         string `yaml:"tool-pair-repair,omitempty" json:"tool-pair-repair,omitempty"`
	InsertSummary          bool   `yaml:"insert-summary,omitempty" json:"insert-summary,omitempty"`
	MaxSummaryBytes        int    `yaml:"max-summary-bytes,omitempty" json:"max-summary-bytes,omitempty"`
	PreserveRecentCommands int    `yaml:"preserve-recent-commands,omitempty" json:"preserve-recent-commands,omitempty"`
	PreserveRecentErrors   int    `yaml:"preserve-recent-errors,omitempty" json:"preserve-recent-errors,omitempty"`
}

type MultimodalAdaptersConfig struct {
	Enabled           *bool                       `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	DefaultAction     string                      `yaml:"default-action,omitempty" json:"default-action,omitempty"`
	UnavailableAction string                      `yaml:"unavailable-action,omitempty" json:"unavailable-action,omitempty"`
	InjectAs          string                      `yaml:"inject-as,omitempty" json:"inject-as,omitempty"`
	MaxMediaItems     int                         `yaml:"max-media-items,omitempty" json:"max-media-items,omitempty"`
	MaxOutputBytes    int                         `yaml:"max-output-bytes,omitempty" json:"max-output-bytes,omitempty"`
	Rules             []MultimodalAdapterRule     `yaml:"rules,omitempty" json:"rules,omitempty"`
	Extractors        []MultimodalExtractorConfig `yaml:"extractors,omitempty" json:"extractors,omitempty"`
}

type MultimodalAdapterRule struct {
	Name              string                 `yaml:"name,omitempty" json:"name,omitempty"`
	Match             MultimodalAdapterMatch `yaml:"match,omitempty" json:"match,omitempty"`
	Extractor         string                 `yaml:"extractor,omitempty" json:"extractor,omitempty"`
	Action            string                 `yaml:"action,omitempty" json:"action,omitempty"`
	UnavailableAction string                 `yaml:"unavailable-action,omitempty" json:"unavailable-action,omitempty"`
	InjectAs          string                 `yaml:"inject-as,omitempty" json:"inject-as,omitempty"`
	MaxMediaItems     int                    `yaml:"max-media-items,omitempty" json:"max-media-items,omitempty"`
	MaxOutputBytes    int                    `yaml:"max-output-bytes,omitempty" json:"max-output-bytes,omitempty"`
}

type MultimodalAdapterMatch struct {
	RequestedModels   []string `yaml:"requested-models,omitempty" json:"requested-models,omitempty"`
	UpstreamProviders []string `yaml:"upstream-providers,omitempty" json:"upstream-providers,omitempty"`
	UpstreamModels    []string `yaml:"upstream-models,omitempty" json:"upstream-models,omitempty"`
	Protocols         []string `yaml:"protocols,omitempty" json:"protocols,omitempty"`
}

type MultimodalExtractorConfig struct {
	Name           string            `yaml:"name,omitempty" json:"name,omitempty"`
	Type           string            `yaml:"type,omitempty" json:"type,omitempty"`
	Endpoint       string            `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Command        string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args           []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env            map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	ToolName       string            `yaml:"tool-name,omitempty" json:"tool-name,omitempty"`
	TimeoutSeconds int               `yaml:"timeout-seconds,omitempty" json:"timeout-seconds,omitempty"`
	Prompt         string            `yaml:"prompt,omitempty" json:"prompt,omitempty"`
}

type ObservabilityConfig struct {
	ResponseTrace ResponseTraceConfig `yaml:"response-trace,omitempty" json:"response-trace,omitempty"`
}

type ResponseTraceConfig struct {
	Enabled             bool `yaml:"enabled" json:"enabled"`
	SlowThresholdMS     int  `yaml:"slow-threshold-ms,omitempty" json:"slow-threshold-ms,omitempty"`
	LogPayloadPreview   bool `yaml:"log-payload-preview,omitempty" json:"log-payload-preview,omitempty"`
	PayloadPreviewBytes int  `yaml:"payload-preview-bytes,omitempty" json:"payload-preview-bytes,omitempty"`
	LogHeaders          bool `yaml:"log-headers,omitempty" json:"log-headers,omitempty"`
}
