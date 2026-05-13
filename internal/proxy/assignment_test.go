package proxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestAssignmentEngineReassignUnavailableProxy(t *testing.T) {
	engine := NewAssignmentEngine(config.ProxyAssignmentConfig{
		Enabled:  true,
		Strategy: config.ProxyAssignmentRoundRobin,
	}, []config.ProxyPoolEntry{
		{ID: "proxy-a", URL: "http://proxy-a.local:8080", Enabled: true},
		{ID: "proxy-b", URL: "http://proxy-b.local:8080", Enabled: true},
	})

	auths := []*coreauth.Auth{
		{ID: "stale", ProxyID: "deleted-proxy", Metadata: map[string]any{"proxy_id": "deleted-proxy"}},
		{ID: "kept", ProxyID: "proxy-a", Metadata: map[string]any{"proxy_id": "proxy-a"}},
		{ID: "manual-url", ProxyURL: "http://manual.local:8080"},
	}

	changed := engine.ReassignUnavailable(auths)
	if len(changed) != 1 {
		t.Fatalf("changed = %d, want 1", len(changed))
	}
	if changed[0].ID != "stale" {
		t.Fatalf("changed auth = %s, want stale", changed[0].ID)
	}
	if auths[0].ProxyID == "" || auths[0].ProxyID == "deleted-proxy" {
		t.Fatalf("stale auth proxy_id = %q, want reassigned proxy", auths[0].ProxyID)
	}
	if got := auths[0].Metadata["proxy_id"]; got != auths[0].ProxyID {
		t.Fatalf("metadata proxy_id = %#v, want %q", got, auths[0].ProxyID)
	}
	if auths[1].ProxyID != "proxy-a" {
		t.Fatalf("kept auth proxy_id = %q, want proxy-a", auths[1].ProxyID)
	}
	if auths[2].ProxyID != "" {
		t.Fatalf("manual URL auth proxy_id = %q, want empty", auths[2].ProxyID)
	}
}
