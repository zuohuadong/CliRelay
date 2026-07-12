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

func TestLoadConfigOptionalResponsesMaxInboundBytesDefaultsWhenUnset(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("debug: false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if cfg.ResponsesMaxInboundBytes != DefaultResponsesMaxInboundBytes {
		t.Fatalf("ResponsesMaxInboundBytes = %d, want %d", cfg.ResponsesMaxInboundBytes, DefaultResponsesMaxInboundBytes)
	}
}
