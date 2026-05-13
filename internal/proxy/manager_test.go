package proxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type proxyManagerTestMutator struct {
	pool []config.ProxyPoolEntry
}

func (m *proxyManagerTestMutator) DisableProxy(proxyID string) bool {
	for idx := range m.pool {
		if m.pool[idx].ID == proxyID {
			m.pool[idx].Enabled = false
			return true
		}
	}
	return false
}

func (m *proxyManagerTestMutator) EnableProxy(proxyID string) bool {
	for idx := range m.pool {
		if m.pool[idx].ID == proxyID {
			m.pool[idx].Enabled = true
			return true
		}
	}
	return false
}

func (m *proxyManagerTestMutator) GetPool() []config.ProxyPoolEntry {
	out := make([]config.ProxyPoolEntry, len(m.pool))
	copy(out, m.pool)
	return out
}

type proxyManagerTestAuthStore struct {
	auths   []*coreauth.Auth
	updates []*coreauth.Auth
}

func (s *proxyManagerTestAuthStore) ListAuths() []*coreauth.Auth {
	return s.auths
}

func (s *proxyManagerTestAuthStore) UpdateAuth(auth *coreauth.Auth) error {
	s.updates = append(s.updates, auth)
	return nil
}

func TestProxyManagerOnConfigReloadReassignsRemovedProxy(t *testing.T) {
	mutator := &proxyManagerTestMutator{
		pool: []config.ProxyPoolEntry{
			{ID: "proxy-b", URL: "http://proxy-b.local:8080", Enabled: true},
		},
	}
	store := &proxyManagerTestAuthStore{
		auths: []*coreauth.Auth{
			{ID: "codex-a", Provider: "codex", ProxyID: "proxy-a", Metadata: map[string]any{"proxy_id": "proxy-a"}},
			{ID: "codex-b", Provider: "codex"},
		},
	}
	manager := NewProxyManager(config.ProxyManagerConfig{
		Assignment: config.ProxyAssignmentConfig{
			Enabled:   true,
			Strategy:  config.ProxyAssignmentRoundRobin,
			Providers: []string{"codex"},
		},
	}, mutator, store, &config.SDKConfig{})

	manager.OnConfigReload(&config.Config{
		ProxyManager: config.ProxyManagerConfig{
			Assignment: config.ProxyAssignmentConfig{
				Enabled:   true,
				Strategy:  config.ProxyAssignmentRoundRobin,
				Providers: []string{"codex"},
			},
		},
	})

	if store.auths[0].ProxyID != "proxy-b" {
		t.Fatalf("removed proxy auth proxy_id = %q, want proxy-b", store.auths[0].ProxyID)
	}
	if store.auths[1].ProxyID != "proxy-b" {
		t.Fatalf("unassigned auth proxy_id = %q, want proxy-b", store.auths[1].ProxyID)
	}
	if len(store.updates) != 2 {
		t.Fatalf("updates = %d, want 2", len(store.updates))
	}
}
