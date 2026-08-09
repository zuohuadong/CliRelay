package config

const (
	DefaultPanelGitHubRepository = "https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
	DefaultPprofAddr             = "127.0.0.1:8316"
	DefaultAuthDir               = "~/.cli-proxy-api"
)

// Local custom defaults for Responses memory limits and Codex response header timeout.
const (
	DefaultResponsesMaxInboundBytes             int64 = 32 << 20
	DefaultResponsesMemoryBudgetBytes           int64 = 256 << 20
	DefaultResponsesWebsocketMaxSessionBytes    int64 = 64 << 20
	DefaultResponsesWebsocketMaxTurnOutputBytes int64 = 32 << 20
	DefaultResponsesWebsocketToolCacheBytes     int64 = 8 << 20
	DefaultResponsesWebsocketMemoryBudgetBytes  int64 = 192 << 20
	DefaultResponsesWebsocketMaxConnections           = 0
	DefaultCodexResponseHeaderTimeoutSeconds          = 90
)

func normalizeResponsesMaxInboundBytes(value int64) int64 {
	if value <= 0 {
		return DefaultResponsesMaxInboundBytes
	}
	return value
}

func normalizePositiveBytes(value, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}
