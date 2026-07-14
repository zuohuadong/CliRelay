package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigBytesCodexResponseHeaderTimeout(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		cfg, err := ParseConfigBytes([]byte("debug: false\n"))
		if err != nil {
			t.Fatalf("ParseConfigBytes() error = %v", err)
		}
		if got := cfg.Codex.ResponseHeaderTimeoutSeconds; got != DefaultCodexResponseHeaderTimeoutSeconds {
			t.Fatalf("ResponseHeaderTimeoutSeconds = %d, want default %d", got, DefaultCodexResponseHeaderTimeoutSeconds)
		}
	})

	t.Run("override", func(t *testing.T) {
		cfg, err := ParseConfigBytes([]byte("codex:\n  response-header-timeout-seconds: 45\n"))
		if err != nil {
			t.Fatalf("ParseConfigBytes() error = %v", err)
		}
		if got := cfg.Codex.ResponseHeaderTimeoutSeconds; got != 45 {
			t.Fatalf("ResponseHeaderTimeoutSeconds = %d, want 45", got)
		}
	})

	t.Run("negative disables", func(t *testing.T) {
		cfg, err := ParseConfigBytes([]byte("codex:\n  response-header-timeout-seconds: -1\n"))
		if err != nil {
			t.Fatalf("ParseConfigBytes() error = %v", err)
		}
		if got := cfg.Codex.ResponseHeaderTimeoutSeconds; got != -1 {
			t.Fatalf("ResponseHeaderTimeoutSeconds = %d, want -1", got)
		}
	})
}

func TestLoadConfigOptionalCodexResponseHeaderTimeoutDefault(t *testing.T) {
	t.Run("missing optional file", func(t *testing.T) {
		cfg, err := LoadConfigOptional(filepath.Join(t.TempDir(), "missing.yaml"), true)
		if err != nil {
			t.Fatalf("LoadConfigOptional() error = %v", err)
		}
		if got := cfg.Codex.ResponseHeaderTimeoutSeconds; got != DefaultCodexResponseHeaderTimeoutSeconds {
			t.Fatalf("ResponseHeaderTimeoutSeconds = %d, want default %d", got, DefaultCodexResponseHeaderTimeoutSeconds)
		}
	})

	t.Run("empty optional file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		cfg, err := LoadConfigOptional(path, true)
		if err != nil {
			t.Fatalf("LoadConfigOptional() error = %v", err)
		}
		if got := cfg.Codex.ResponseHeaderTimeoutSeconds; got != DefaultCodexResponseHeaderTimeoutSeconds {
			t.Fatalf("ResponseHeaderTimeoutSeconds = %d, want default %d", got, DefaultCodexResponseHeaderTimeoutSeconds)
		}
	})
}
