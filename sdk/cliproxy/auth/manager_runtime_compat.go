package auth

import (
	"strings"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// SetConfigForTenant publishes routing and model settings for one tenant.
// The legacy process-wide config remains available to refresh and lifecycle
// code; tenant selection reads the immutable snapshot published here.
func (m *Manager) SetConfigForTenant(tenantID string, cfg *sdkconfig.Config) {
	if m == nil {
		return
	}
	tenantID = normalizedTenantID(tenantID)
	next := runtimeConfigSnapshotSet{}
	if current, ok := m.tenantRuntimeConfigs.Load().(runtimeConfigSnapshotSet); ok {
		for key, value := range current {
			next[key] = value
		}
	}
	next[tenantID] = newRuntimeConfigSnapshot(cfg)
	m.tenantRuntimeConfigs.Store(next)
	if tenantID == defaultTenantID {
		// Keep legacy callers in sync with the default tenant.
		m.SetConfig(cfg)
		return
	}
	m.rebuildAPIKeyModelAliasFromRuntimeConfig()
}

// SetQuotaLoadSource wires live quota windows into the scheduler dependencies
// used by quota-aware selectors and session affinity.
func (m *Manager) SetQuotaLoadSource(source QuotaLoadSource) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.sessionSchedulerDeps == nil {
		m.sessionSchedulerDeps = &schedulerDeps{
			tracker: newSelectionPressureTracker(),
			limiter: m.concurrencyLimiter,
		}
	}
	m.sessionSchedulerDeps.quota = source
	m.mu.Unlock()
}

// distributionSelectorLocked resolves the named distribution selector used by
// compatibility callers. Reset-aware routing is enabled when quota data is
// available; other names use the manager's configured selector.
func (m *Manager) distributionSelectorLocked(name string) Selector {
	if m == nil {
		return &RoundRobinSelector{}
	}
	m.mu.RLock()
	base := m.selector
	deps := m.sessionSchedulerDeps
	m.mu.RUnlock()
	if strings.EqualFold(strings.TrimSpace(name), "reset-aware") && deps != nil && deps.quotaWindows() != nil {
		return &ResetAwareSelector{deps: deps}
	}
	if base == nil {
		return &RoundRobinSelector{}
	}
	return base
}

// ExecutorForTenant returns the provider executor registered for one tenant.
func (m *Manager) ExecutorForTenant(tenantID, provider string) (ProviderExecutor, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	exec := m.executorForTenantLocked(strings.TrimSpace(tenantID), strings.TrimSpace(provider))
	return exec, exec != nil
}
