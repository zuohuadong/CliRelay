package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestMaskKey(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"", ""},
		{"short", "****"},
		{"sk-user1234567890abcd", "sk-use****abcd"},
	}
	for _, tc := range tests {
		if got := maskKey(tc.input); got != tc.expected {
			t.Errorf("maskKey(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestMaskName(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"", ""},
		{"A", "***"},
		{"管理员", "管***"},
		{"周肖杰", "周***"},
		{"Alice", "A***"},
	}
	for _, tc := range tests {
		if got := maskName(tc.input); got != tc.expected {
			t.Errorf("maskName(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestMaskBaseURL(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"", ""},
		{"https://api.tabcode.cc/openai", "https://api.tabcode.cc/***"},
		{"https://chat.tabcode.cc", "https://chat.tabcode.cc/***"},
		{"http://localhost:8080/v1/chat", "http://localhost:8080/***"},
		{"not-a-url", "***"},
	}
	for _, tc := range tests {
		if got := maskBaseURL(tc.input); got != tc.expected {
			t.Errorf("maskBaseURL(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestSanitizeConfigForAPI(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL: "http://proxy.example.com:8080/path",
			APIKeys:  []string{"sk-user1234567890abcd"},
			APIKeyEntries: []config.APIKeyEntry{
				{Key: "sk-user1234567890abcd", Name: "管理员"},
				{Key: "sk-gauXXXXb41v00000000", Name: "周肖杰"},
			},
		},
		Redis: config.RedisConfig{
			Enable:   true,
			Addr:     "127.0.0.1:6379",
			Password: "secret",
		},
		TLS: config.TLSConfig{
			Enable: false,
			Cert:   "/etc/ssl/cert.pem",
			Key:    "/etc/ssl/key.pem",
		},
		Pprof: config.PprofConfig{
			Addr: "127.0.0.1:8316",
		},
		GeminiKey: []config.GeminiKey{
			{
				APIKey:  "sk-mJdvXXXXm0dV000000",
				Name:    "Gemini公益",
				BaseURL: "https://chat.tabcode.cc",
				Models: []config.GeminiModel{
					{Name: "gemini-3.1-flash", Alias: ""},
				},
			},
		},
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey:  "sk-useXXXX586d000000",
				Name:    "GLM渠道",
				BaseURL: "https://api.tabcode.cc/claude/glm",
				Models: []config.ClaudeModel{
					{Name: "glm-5", Alias: ""},
				},
			},
		},
		CodexKey: []config.CodexKey{
			{
				APIKey:  "sk-useXXXXdb42000000",
				Name:    "codex team 代理",
				BaseURL: "https://api.tabcode.cc/openai",
				Models: []config.CodexModel{
					{Name: "gpt-5.4", Alias: ""},
				},
			},
		},
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"antigravity": {
				{Name: "rev19-uic3-1p", Alias: "gemini-2.5-computer-use-preview"},
			},
		},
		AmpCode: config.AmpCode{
			UpstreamURL:    "https://amp.example.com/api",
			UpstreamAPIKey: "sk-ampXXXX1234000000",
			ModelMappings: []config.AmpModelMapping{
				{From: "claude-opus-4.5", To: "claude-sonnet-4"},
			},
		},
	}

	sanitized := sanitizeConfigForAPI(cfg)

	// ── Verify API keys are masked ──
	for _, k := range sanitized.APIKeys {
		if k == "sk-user1234567890abcd" {
			t.Error("APIKeys: raw key not masked")
		}
	}

	// ── Verify API key entry names are masked ──
	for _, e := range sanitized.APIKeyEntries {
		if e.Name == "管理员" || e.Name == "周肖杰" {
			t.Errorf("APIKeyEntries: name %q not masked", e.Name)
		}
		if e.Key == "sk-user1234567890abcd" || e.Key == "sk-gauXXXXb41v00000000" {
			t.Errorf("APIKeyEntries: key %q not masked", e.Key)
		}
	}

	// ── Verify Gemini keys ──
	for _, g := range sanitized.GeminiKey {
		if g.Name == "Gemini公益" {
			t.Error("GeminiKey: name not masked")
		}
		if g.BaseURL == "https://chat.tabcode.cc" {
			t.Error("GeminiKey: base-url not masked")
		}
		if g.Models != nil {
			t.Error("GeminiKey: models not cleared")
		}
	}

	// ── Verify Claude keys ──
	for _, c := range sanitized.ClaudeKey {
		if c.Name == "GLM渠道" {
			t.Error("ClaudeKey: name not masked")
		}
		if c.BaseURL == "https://api.tabcode.cc/claude/glm" {
			t.Error("ClaudeKey: base-url not masked")
		}
		if c.Models != nil {
			t.Error("ClaudeKey: models not cleared")
		}
	}

	// ── Verify Codex keys ──
	for _, c := range sanitized.CodexKey {
		if c.Name == "codex team 代理" {
			t.Error("CodexKey: name not masked")
		}
		if c.BaseURL == "https://api.tabcode.cc/openai" {
			t.Error("CodexKey: base-url not masked")
		}
		if c.Models != nil {
			t.Error("CodexKey: models not cleared")
		}
	}

	// ── Verify OAuthModelAlias is stripped ──
	if sanitized.OAuthModelAlias != nil {
		t.Error("OAuthModelAlias not cleared")
	}

	// ── Verify Redis addr is masked ──
	if sanitized.Redis.Addr == "127.0.0.1:6379" {
		t.Error("Redis.Addr not masked")
	}
	if sanitized.Redis.Password != "***" {
		t.Error("Redis.Password not masked")
	}

	// ── Verify pprof addr is masked ──
	if sanitized.Pprof.Addr == "127.0.0.1:8316" {
		t.Error("Pprof.Addr not masked")
	}

	// ── Verify TLS paths are masked ──
	if sanitized.TLS.Cert == "/etc/ssl/cert.pem" {
		t.Error("TLS.Cert not masked")
	}
	if sanitized.TLS.Key == "/etc/ssl/key.pem" {
		t.Error("TLS.Key not masked")
	}

	// ── Verify Amp upstream is masked ──
	if sanitized.AmpCode.UpstreamURL == "https://amp.example.com/api" {
		t.Error("AmpCode.UpstreamURL not masked")
	}
	if sanitized.AmpCode.ModelMappings != nil {
		t.Error("AmpCode.ModelMappings not cleared")
	}

	// ── Verify global ProxyURL is masked ──
	if sanitized.ProxyURL == "http://proxy.example.com:8080/path" {
		t.Error("ProxyURL not masked")
	}

	// ── Verify original config is NOT modified ──
	if cfg.Redis.Addr != "127.0.0.1:6379" {
		t.Error("original config was mutated: Redis.Addr")
	}
	if cfg.APIKeyEntries[0].Name != "管理员" {
		t.Error("original config was mutated: APIKeyEntries[0].Name")
	}
	if cfg.OAuthModelAlias == nil {
		t.Error("original config was mutated: OAuthModelAlias")
	}
}
