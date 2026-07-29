package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestApplyKimiHeadersUsesConfiguredIdentity(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://api.kimi.com/coding/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	cfg := &config.Config{KimiHeaderDefaults: config.KimiHeaderDefaults{
		UserAgent:   "custom-client",
		Platform:    "custom-platform",
		Version:     "2.0.0",
		DeviceName:  "relay-node",
		DeviceModel: "virtual",
	}}

	applyKimiHeaders(req, "token", false, cfg)

	want := map[string]string{
		"User-Agent":         "custom-client",
		"X-Msh-Platform":     "custom-platform",
		"X-Msh-Version":      "2.0.0",
		"X-Msh-Device-Name":  "relay-node",
		"X-Msh-Device-Model": "virtual",
	}
	for name, expected := range want {
		if got := req.Header.Get(name); got != expected {
			t.Fatalf("%s = %q, want %q", name, got, expected)
		}
	}
}

func TestApplyKimiHeadersUsesPrivacyDefaults(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://api.kimi.com/coding/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	applyKimiHeaders(req, "token", true, nil)

	for _, name := range []string{"User-Agent", "X-Msh-Platform", "X-Msh-Device-Name", "X-Msh-Device-Model"} {
		if got := req.Header.Get(name); got != "codex" {
			t.Fatalf("%s = %q, want codex", name, got)
		}
	}
	if got := req.Header.Get("X-Msh-Version"); got != "1.0.0" {
		t.Fatalf("X-Msh-Version = %q, want 1.0.0", got)
	}
	if got := req.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", got)
	}
}
