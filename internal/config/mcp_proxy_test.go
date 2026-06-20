package config

import (
	"os"
	"path/filepath"
	"testing"
)

const mcpProxySanitizeYAML = `
mcp-proxy:
  servers:
    - name: " DevSpace "
      base-url: " http://127.0.0.1:7676/mcp "
      headers:
        X-Owner: " owner-token "
        X-Empty: " "
    - name: "devspace"
      base-url: "http://127.0.0.1:7677/mcp"
    - name: ""
      base-url: "http://127.0.0.1:7678/mcp"
    - name: "bad"
      base-url: "://bad"
`

func TestParseConfigBytesSanitizesMCPProxyServers(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(mcpProxySanitizeYAML))
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	assertSingleSanitizedMCPProxyServer(t, cfg)
}

func TestLoadConfigOptionalSanitizesMCPProxyServers(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(mcpProxySanitizeYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional: %v", err)
	}
	assertSingleSanitizedMCPProxyServer(t, cfg)
}

func assertSingleSanitizedMCPProxyServer(t *testing.T, cfg *Config) {
	t.Helper()

	if len(cfg.MCPProxy.Servers) != 1 {
		t.Fatalf("servers = %#v, want one sanitized entry", cfg.MCPProxy.Servers)
	}
	server := cfg.MCPProxy.Servers[0]
	if server.Name != "devspace" {
		t.Fatalf("server name = %q", server.Name)
	}
	if server.BaseURL != "http://127.0.0.1:7676/mcp" {
		t.Fatalf("base-url = %q", server.BaseURL)
	}
	if got := server.Headers["X-Owner"]; got != "owner-token" {
		t.Fatalf("X-Owner = %q", got)
	}
	if _, exists := server.Headers["X-Empty"]; exists {
		t.Fatalf("empty header was preserved: %#v", server.Headers)
	}
}
