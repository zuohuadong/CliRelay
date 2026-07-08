package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/safemode"
)

func TestShouldStartExampleAPIKeyWarningServer(t *testing.T) {
	cfgWithExampleKey := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"real-key", " your-api-key-1 "},
		},
	}
	cfgWithRealKey := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"real-key"},
		},
	}

	tests := []struct {
		name               string
		cfg                *config.Config
		commandMode        bool
		tuiMode            bool
		standalone         bool
		cloudConfigMissing bool
		homeMode           bool
		want               bool
	}{
		{
			name: "normal server with example key",
			cfg:  cfgWithExampleKey,
			want: true,
		},
		{
			name:       "standalone tui with example key",
			cfg:        cfgWithExampleKey,
			tuiMode:    true,
			standalone: true,
			want:       true,
		},
		{
			name:        "pure tui client is not blocked",
			cfg:         cfgWithExampleKey,
			tuiMode:     true,
			standalone:  false,
			commandMode: false,
			want:        false,
		},
		{
			name:        "one-shot command is not blocked",
			cfg:         cfgWithExampleKey,
			commandMode: true,
			want:        false,
		},
		{
			name:     "home mode is not blocked",
			cfg:      cfgWithExampleKey,
			homeMode: true,
			want:     false,
		},
		{
			name:               "cloud standby without config is not blocked",
			cfg:                cfgWithExampleKey,
			cloudConfigMissing: true,
			want:               false,
		},
		{
			name: "normal server with real key",
			cfg:  cfgWithRealKey,
			want: false,
		},
		{
			name: "nil config",
			cfg:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldStartExampleAPIKeyWarningServer(tt.cfg, tt.commandMode, tt.tuiMode, tt.standalone, tt.cloudConfigMissing, tt.homeMode)
			if got != tt.want {
				t.Fatalf("shouldStartExampleAPIKeyWarningServer() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestBootstrapLocalConfigIfMissingUsesEnvironment(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "mgmt-from-env")
	t.Setenv("CLI_PROXY_API_KEY", "api-from-env")
	t.Setenv("PORT", "9123")
	t.Setenv("CLI_PROXY_AUTH_DIR", "/tmp/cliproxy-auth")

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	secrets, err := bootstrapLocalConfigIfMissing(configPath, testLookupEnv)
	if err != nil {
		t.Fatalf("bootstrapLocalConfigIfMissing() error = %v", err)
	}
	if secrets == nil {
		t.Fatal("bootstrapLocalConfigIfMissing() returned nil secrets")
	}
	if secrets.GeneratedAPIKey {
		t.Fatal("expected API key from environment")
	}
	if secrets.APIKey != "api-from-env" {
		t.Fatalf("APIKey = %q, want %q", secrets.APIKey, "api-from-env")
	}
	if secrets.GeneratedManagement {
		t.Fatal("expected management password from environment")
	}
	if secrets.ManagementPassword != "mgmt-from-env" {
		t.Fatalf("ManagementPassword = %q, want %q", secrets.ManagementPassword, "mgmt-from-env")
	}

	cfg, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if cfg.Port != 9123 {
		t.Fatalf("Port = %d, want 9123", cfg.Port)
	}
	if cfg.AuthDir != "/tmp/cliproxy-auth" {
		t.Fatalf("AuthDir = %q, want /tmp/cliproxy-auth", cfg.AuthDir)
	}
	if got := cfg.APIKeys; len(got) != 1 || got[0] != "api-from-env" {
		t.Fatalf("APIKeys = %#v, want [api-from-env]", got)
	}
	if !cfg.RemoteManagement.AllowRemote {
		t.Fatal("RemoteManagement.AllowRemote = false, want true")
	}
}

func TestBootstrapLocalConfigIfMissingGeneratesSecrets(t *testing.T) {
	restoreEnv := clearBootstrapEnv(t)
	defer restoreEnv()

	configPath := filepath.Join(t.TempDir(), "nested", "config.yaml")
	secrets, err := bootstrapLocalConfigIfMissing(configPath, testLookupEnv)
	if err != nil {
		t.Fatalf("bootstrapLocalConfigIfMissing() error = %v", err)
	}
	if secrets == nil {
		t.Fatal("bootstrapLocalConfigIfMissing() returned nil secrets")
	}
	if !secrets.GeneratedAPIKey {
		t.Fatal("expected generated API key")
	}
	if !strings.HasPrefix(secrets.APIKey, "cpak-") {
		t.Fatalf("generated API key = %q, want cpak-*", secrets.APIKey)
	}
	if !secrets.GeneratedManagement {
		t.Fatal("expected generated management password")
	}
	if !strings.HasPrefix(secrets.ManagementPassword, "cpmg-") {
		t.Fatalf("generated management password = %q, want cpmg-*", secrets.ManagementPassword)
	}
	if got := os.Getenv("MANAGEMENT_PASSWORD"); got != secrets.ManagementPassword {
		t.Fatalf("MANAGEMENT_PASSWORD = %q, want generated password", got)
	}

	cfg, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if cfg.Port != 8317 {
		t.Fatalf("Port = %d, want 8317", cfg.Port)
	}
	if got := cfg.APIKeys; len(got) != 1 || got[0] != secrets.APIKey {
		t.Fatalf("APIKeys = %#v, want generated API key", got)
	}
	if safemode.HasExampleAPIKeys(cfg.APIKeys) {
		t.Fatalf("generated config contains example API keys: %#v", cfg.APIKeys)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("Stat(configPath) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config file perm = %o, want 600", got)
	}
}

func TestBootstrapLocalConfigIfMissingDoesNotOverwriteExistingConfig(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "mgmt-from-env")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("port: 9000\napi-keys:\n  - existing\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	secrets, err := bootstrapLocalConfigIfMissing(configPath, testLookupEnv)
	if err != nil {
		t.Fatalf("bootstrapLocalConfigIfMissing() error = %v", err)
	}
	if secrets != nil {
		t.Fatalf("secrets = %#v, want nil for existing config", secrets)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != string(original) {
		t.Fatalf("config changed:\n%s", string(data))
	}
}

func testLookupEnv(keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed, true
			}
		}
	}
	return "", false
}

func clearBootstrapEnv(t *testing.T) func() {
	t.Helper()
	keys := []string{
		"MANAGEMENT_PASSWORD",
		"management_password",
		"CLI_PROXY_API_KEY",
		"API_KEY",
		"api_key",
		"PORT",
		"CLI_PROXY_PORT",
		"port",
		"CLI_PROXY_AUTH_DIR",
		"AUTH_DIR",
		"auth_dir",
	}
	original := make(map[string]*string, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			valueCopy := value
			original[key] = &valueCopy
		} else {
			original[key] = nil
		}
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("Unsetenv(%s) error = %v", key, err)
		}
	}
	return func() {
		for _, key := range keys {
			if value := original[key]; value != nil {
				_ = os.Setenv(key, *value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}
}
