package config

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseConfigBytesIgnoresRemovedReusableProxyFields(t *testing.T) {
	legacyPoolKey := "proxy-" + "pool"
	legacyIDKey := "proxy-" + "id"
	payload := fmt.Sprintf(`
%s:
  - id: legacy-egress
    name: Legacy egress
    url: http://legacy-proxy.example:8080
    enabled: true
opencode-go-api-key:
  - api-key: test-key
    proxy-url: http://direct-proxy.example:8080
    %s: legacy-egress
`, legacyPoolKey, legacyIDKey)

	cfg, err := ParseConfigBytes([]byte(payload))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.OpenCodeGoKey) != 1 {
		t.Fatalf("OpenCodeGoKey length = %d, want 1", len(cfg.OpenCodeGoKey))
	}
	if got := cfg.OpenCodeGoKey[0].ProxyURL; got != "http://direct-proxy.example:8080" {
		t.Fatalf("ProxyURL = %q, want direct provider proxy", got)
	}

	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	output := string(encoded)
	if strings.Contains(output, legacyPoolKey+":") {
		t.Fatalf("removed reusable proxy list was retained:\n%s", output)
	}
	if strings.Contains(output, legacyIDKey+":") {
		t.Fatalf("removed reusable proxy reference was retained:\n%s", output)
	}
}
