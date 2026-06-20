package config

import (
	"net/url"
	"strings"

	log "github.com/sirupsen/logrus"
)

func (cfg *Config) SanitizeMCPProxy() {
	if cfg == nil || len(cfg.MCPProxy.Servers) == 0 {
		return
	}
	servers := make([]MCPProxyServerConfig, 0, len(cfg.MCPProxy.Servers))
	seen := make(map[string]struct{}, len(cfg.MCPProxy.Servers))
	for _, server := range cfg.MCPProxy.Servers {
		server.Name = strings.ToLower(strings.TrimSpace(server.Name))
		server.BaseURL = strings.TrimSpace(server.BaseURL)
		if server.Name == "" || server.BaseURL == "" {
			continue
		}
		if _, exists := seen[server.Name]; exists {
			log.WithField("server", server.Name).Warn("duplicate mcp-proxy server ignored")
			continue
		}
		parsed, err := url.Parse(server.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			log.WithField("server", server.Name).Warn("invalid mcp-proxy base-url ignored")
			continue
		}
		server.Headers = sanitizeMCPProxyHeaders(server.Headers)
		seen[server.Name] = struct{}{}
		servers = append(servers, server)
	}
	cfg.MCPProxy.Servers = servers
}

func sanitizeMCPProxyHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
