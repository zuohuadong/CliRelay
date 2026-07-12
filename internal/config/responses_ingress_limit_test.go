package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigBytesResponsesMaxInboundBytes(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfigBytes([]byte("responses-max-inbound-bytes: 12345\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.ResponsesMaxInboundBytes != 12345 {
		t.Fatalf("ResponsesMaxInboundBytes = %d, want 12345", cfg.ResponsesMaxInboundBytes)
	}
}

func TestParseConfigBytesResponsesMaxInboundBytesDefaultsWhenUnset(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfigBytes([]byte("debug: false\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.ResponsesMaxInboundBytes != DefaultResponsesMaxInboundBytes {
		t.Fatalf("ResponsesMaxInboundBytes = %d, want %d", cfg.ResponsesMaxInboundBytes, DefaultResponsesMaxInboundBytes)
	}
}

func TestParseConfigBytesResponsesMemoryBudgetsDefaultWhenUnset(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.ResponsesMaxInboundBytes != 32<<20 {
		t.Fatalf("ResponsesMaxInboundBytes = %d, want %d", cfg.ResponsesMaxInboundBytes, int64(32<<20))
	}
	if cfg.ResponsesMemoryBudgetBytes != 256<<20 {
		t.Fatalf("ResponsesMemoryBudgetBytes = %d, want %d", cfg.ResponsesMemoryBudgetBytes, int64(256<<20))
	}
	if cfg.ResponsesWebsocketMaxSessionBytes != 64<<20 {
		t.Fatalf("ResponsesWebsocketMaxSessionBytes = %d, want %d", cfg.ResponsesWebsocketMaxSessionBytes, int64(64<<20))
	}
	if cfg.ResponsesWebsocketMaxTurnOutputBytes != 32<<20 {
		t.Fatalf("ResponsesWebsocketMaxTurnOutputBytes = %d, want %d", cfg.ResponsesWebsocketMaxTurnOutputBytes, int64(32<<20))
	}
	if cfg.ResponsesWebsocketToolCacheBytes != 8<<20 {
		t.Fatalf("ResponsesWebsocketToolCacheBytes = %d, want %d", cfg.ResponsesWebsocketToolCacheBytes, int64(8<<20))
	}
	if cfg.ResponsesWebsocketMemoryBudgetBytes != 192<<20 {
		t.Fatalf("ResponsesWebsocketMemoryBudgetBytes = %d, want %d", cfg.ResponsesWebsocketMemoryBudgetBytes, int64(192<<20))
	}
	if cfg.ResponsesWebsocketMaxConnections != 4 {
		t.Fatalf("ResponsesWebsocketMaxConnections = %d, want 4", cfg.ResponsesWebsocketMaxConnections)
	}
}

func TestParseConfigBytesResponsesMemoryBudgetsNormalizeNonPositive(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
responses-max-inbound-bytes: 0
responses-memory-budget-bytes: -1
responses-websocket-max-session-bytes: 0
responses-websocket-max-turn-output-bytes: -10
responses-websocket-tool-cache-bytes: 0
responses-websocket-memory-budget-bytes: -1
responses-websocket-max-connections: 0
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.ResponsesMaxInboundBytes != DefaultResponsesMaxInboundBytes ||
		cfg.ResponsesMemoryBudgetBytes != DefaultResponsesMemoryBudgetBytes ||
		cfg.ResponsesWebsocketMaxSessionBytes != DefaultResponsesWebsocketMaxSessionBytes ||
		cfg.ResponsesWebsocketMaxTurnOutputBytes != DefaultResponsesWebsocketMaxTurnOutputBytes ||
		cfg.ResponsesWebsocketToolCacheBytes != DefaultResponsesWebsocketToolCacheBytes ||
		cfg.ResponsesWebsocketMemoryBudgetBytes != DefaultResponsesWebsocketMemoryBudgetBytes ||
		cfg.ResponsesWebsocketMaxConnections != DefaultResponsesWebsocketMaxConnections {
		t.Fatalf("non-positive responses memory settings were not normalized: %+v", cfg.SDKConfig)
	}
}

func TestLoadConfigOptionalResponsesMaxInboundBytesDefaultsWhenUnset(t *testing.T) {
	tests := []struct {
		name     string
		optional bool
		missing  bool
		content  string
	}{
		{name: "unset", content: "debug: false\n"},
		{name: "non-positive", content: "responses-max-inbound-bytes: 0\nresponses-memory-budget-bytes: -1\nresponses-websocket-max-session-bytes: 0\nresponses-websocket-max-turn-output-bytes: -1\nresponses-websocket-tool-cache-bytes: 0\n"},
		{name: "missing optional", optional: true, missing: true},
		{name: "empty optional", optional: true, content: ""},
		{name: "invalid optional", optional: true, content: ": invalid: yaml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if !tc.missing {
				if err := os.WriteFile(configPath, []byte(tc.content), 0o600); err != nil {
					t.Fatalf("write config: %v", err)
				}
			}
			cfg, err := LoadConfigOptional(configPath, tc.optional)
			if err != nil {
				t.Fatalf("LoadConfigOptional() error = %v", err)
			}
			assertResponsesMemoryDefaults(t, cfg)
		})
	}
}

func assertResponsesMemoryDefaults(t *testing.T, cfg *Config) {
	t.Helper()
	if cfg.ResponsesMaxInboundBytes != DefaultResponsesMaxInboundBytes ||
		cfg.ResponsesMemoryBudgetBytes != DefaultResponsesMemoryBudgetBytes ||
		cfg.ResponsesWebsocketMaxSessionBytes != DefaultResponsesWebsocketMaxSessionBytes ||
		cfg.ResponsesWebsocketMaxTurnOutputBytes != DefaultResponsesWebsocketMaxTurnOutputBytes ||
		cfg.ResponsesWebsocketToolCacheBytes != DefaultResponsesWebsocketToolCacheBytes ||
		cfg.ResponsesWebsocketMemoryBudgetBytes != DefaultResponsesWebsocketMemoryBudgetBytes ||
		cfg.ResponsesWebsocketMaxConnections != DefaultResponsesWebsocketMaxConnections {
		t.Fatalf("responses memory defaults not applied: %+v", cfg.SDKConfig)
	}
}
