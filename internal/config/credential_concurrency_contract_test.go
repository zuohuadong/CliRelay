package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Home 是并发策略的唯一权威来源；本测试固定节点接收并校验完整生命周期配置的公共契约。
func TestLoadConfigOptionalCredentialConcurrencyContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
credential-concurrency:
  lifecycle-config-revision: 7
  observation-barrier-revision: 3
  cpa-heartbeat-timeout: 3s
  cpa-cancel-bound: 5s
  reclaim-grace: 6s
  cleanup-interval: 5s
  release-flush-interval: 250ms
  release-max-backoff: 2s
  busy-retry-min: 250ms
  busy-retry-max: 1s
  max-limit: 128
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	got := cfg.CredentialConcurrency
	if got.LifecycleConfigRevision != 7 || got.ObservationBarrierRevision != 3 {
		t.Fatalf("revisions = %d/%d, want 7/3", got.LifecycleConfigRevision, got.ObservationBarrierRevision)
	}
	if got.CPAHeartbeatTimeout != 3*time.Second || got.CPACancelBound != 5*time.Second || got.ReclaimGrace != 6*time.Second {
		t.Fatalf("lifecycle durations = %s/%s/%s", got.CPAHeartbeatTimeout, got.CPACancelBound, got.ReclaimGrace)
	}
	if got.ReleaseFlushInterval != 250*time.Millisecond || got.ReleaseMaxBackoff != 2*time.Second {
		t.Fatalf("release timings = %s/%s", got.ReleaseFlushInterval, got.ReleaseMaxBackoff)
	}
	if got.BusyRetryMin != 250*time.Millisecond || got.BusyRetryMax != time.Second || got.MaxLimit != 128 {
		t.Fatalf("admission config = %s/%s/%d", got.BusyRetryMin, got.BusyRetryMax, got.MaxLimit)
	}
}
