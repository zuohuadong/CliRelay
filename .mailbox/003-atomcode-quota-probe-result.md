```yaml
verdict: PASS
summary: |
  Implemented upstream-style scheduled quota recovery probing for this fork:
  1. Added quota_reconcile.go with QuotaProbeResult, QuotaRecoveryProber interface,
     shouldProbeQuota, applyQuotaProbeResult, ReconcileQuota, probeAndApplyQuota,
     ResumeRegistryModel, probe backoff scheduling, and failure tracking.
  2. Added ProbeQuotaRecovery to CodexExecutor using /backend-api/wham/usage endpoint,
     with conservative rate_limits window parsing via parseCodexUsageQuotaRecovery.
  3. Wired importRegistryResumeClientModel in conductor.go init() to bridge
     quota_reconcile.go to the global model registry without direct imports.
  4. Integrated ReconcileQuota into the auto-refresh loop (auto_refresh_loop.go)
     so quota checks run alongside credential refresh on each tick.
  5. Wired panel_compat.go ReconcileQuota handler to call authManager.ReconcileQuota.
  6. Added quota_reconcile_test.go covering shouldProbeQuota, hasQuotaCooldownModels,
     applyQuotaProbeResult, nextProbeBackoff, ReconcileQuota noop, and clearProbeFailureCount.
changed_files:
  - sdk/cliproxy/auth/quota_reconcile.go (new, 315 lines)
  - sdk/cliproxy/auth/quota_reconcile_test.go (new, 256 lines)
  - sdk/cliproxy/auth/conductor.go (wired importRegistryResumeClientModel in init)
  - sdk/cliproxy/auth/auto_refresh_loop.go (added ReconcileQuota call in loop)
  - internal/runtime/executor/codex_executor.go (added ProbeQuotaRecovery + parseCodexUsageQuotaRecovery)
  - internal/api/handlers/management/panel_compat.go (wired ReconcileQuota handler to authManager)
verification:
  - gofmt: all modified files formatted
  - go build ./cmd/server: PASS
  - go test ./sdk/cliproxy/auth/...: PASS
  - go test ./internal/runtime/executor/...: PASS
  - go test ./...: PASS (all packages)
  - git diff --check: PASS (no whitespace errors)
blocking_findings: []
non_blocking_risks:
  - The /backend-api/wham/usage endpoint response format is undocumented and may change;
    parseCodexUsageQuotaRecovery handles missing/malformed fields conservatively.
  - ProbeQuotaRecovery uses a bare http.Client without proxy/transport customization;
    may need to respect system proxy settings or custom RoundTripper in future.
  - The 30s minimum probe interval and 30m max backoff are constants; could be made
    configurable if operational needs change.
recommended_next_action: |
  The implementation is complete and verified. No further changes needed unless
  operational testing reveals issues with the ChatGPT usage endpoint response format
  or probe timing. Consider adding integration tests with mock HTTP servers for
  ProbeQuotaRecovery if more coverage is desired.
```
