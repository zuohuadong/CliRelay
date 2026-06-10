---
from: orchestrator
to: atomcode-executor
type: subagent-request
status: running
runtime: atomcode
created: 2026-06-10T07:11:00+08:00
---

# AtomCode Executor Request

You are the executor subagent for `/Volumes/Data/workspace/CliRelay`, launched through `atomcode` after the requested `gpt-5.3-codex` subagent process hung without output. Read `AGENTS.md`, `progress.md`, and the relevant repository code before editing. Follow the repository rules.

## Task Contract

Goal:
- Add upstream-style scheduled quota recovery probing to this fork.
- Implement periodic lightweight quota reconciliation for auths currently blocked by quota cooldown.
- Add a generic auth Manager quota probe/reconcile path comparable to upstream kittors/CliRelay `sdk/cliproxy/auth/quota_reconcile.go`.
- Add Codex executor support for `ProbeQuotaRecovery(ctx, auth)` using the lightweight ChatGPT usage endpoint `https://chatgpt.com/backend-api/wham/usage` and parsing recovery/reset information.
- Wire scheduled quota checks into the existing auth auto-refresh loop in this fork without broad architecture reshaping.

Non-goals:
- Do not deploy, commit, push, or change production config/secrets/generated binaries.
- Do not perform a broad upstream refactor or split `conductor.go` unless absolutely required.
- Do not change translator-only behavior.
- Do not implement probe support for providers other than Codex unless needed for clean generic interfaces.

Allowed write scope:
- `sdk/cliproxy/auth/*.go`
- `sdk/cliproxy/auth/*test.go`
- `internal/runtime/executor/codex_executor.go`
- `internal/runtime/executor/codex_executor*_test.go`
- `.mailbox/003-atomcode-quota-probe-result.md`
- `progress.md` only if needed to append executor evidence

Expected direction:
- Reuse upstream concepts: `QuotaProbeResult`, `QuotaProbeModelResult`, optional `QuotaRecoveryProber`, `ReconcileQuota`, `checkQuotaRecoveries`, `shouldProbeQuota`, probe backoff/min/max scheduling, `applyQuotaProbeResult`.
- Adapt to the current fork's existing `auto_refresh_loop.go`/`conductor.go` design rather than wholesale replacing it with upstream split services.
- For Codex, add `ProbeQuotaRecovery` near other request/auth helpers and parse `rate_limits`/window usage conservatively so quota is not cleared prematurely.
- Persist auth updates and fire hooks when probe changes quota state.
- Resume/unblock relevant model/auth state consistently with existing selector behavior.

Verification required before reporting:
- `gofmt` on modified Go files
- `go test ./sdk/cliproxy/auth ./internal/runtime/executor`
- `go test ./...`
- `go build -o test-output ./cmd/server && rm test-output`
- `git diff --check`

Output requirement:
- Write final result or blocker evidence to `.mailbox/003-atomcode-quota-probe-result.md`.
- Use this schema:

```yaml
verdict: PASS|FAIL
summary:
changed_files:
verification:
blocking_findings:
non_blocking_risks:
recommended_next_action:
```

Important coordination:
- You are not alone in the codebase. Do not revert changes made by others. Work with existing changes.
- Keep the diff small and focused.
- If blocked, write concrete evidence to the result mailbox file.
