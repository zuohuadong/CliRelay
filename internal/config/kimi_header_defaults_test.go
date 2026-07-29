package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_KimiHeaderDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := []byte(`
kimi-header-defaults:
  user-agent: "  custom-client  "
  platform: "  custom-platform  "
  version: "  2.0.0  "
  device-name: "  relay-node  "
  device-model: "  virtual  "
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	got := cfg.KimiHeaderDefaults
	if got.UserAgent != "custom-client" ||
		got.Platform != "custom-platform" ||
		got.Version != "2.0.0" ||
		got.DeviceName != "relay-node" ||
		got.DeviceModel != "virtual" {
		t.Fatalf("KimiHeaderDefaults = %#v", got)
	}
}

func TestKimiHeaderDefaultsWithDefaults(t *testing.T) {
	got := (KimiHeaderDefaults{}).WithDefaults()
	if got.UserAgent != "codex" ||
		got.Platform != "codex" ||
		got.Version != "1.0.0" ||
		got.DeviceName != "codex" ||
		got.DeviceModel != "codex" {
		t.Fatalf("defaults = %#v", got)
	}
}
