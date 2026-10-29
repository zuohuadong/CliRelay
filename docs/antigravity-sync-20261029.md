# Antigravity compatibility sync (2026-10-29)

This sync ports Antigravity fixes from CLIProxyAPI through `90c447d3a` and
kittors/CliRelay through `11d8df955`. It is scoped to Antigravity compatibility;
the two upstream branches contain other changes that are outside this sync.

References:

- CLIProxyAPI: `02e9e8520`, `80aa2b1d1`, `da21f1e58`, `ede07cafa`,
  `37bd18b08`, `7517549a6`, `90c447d3a`.
- kittors/CliRelay: `452e8a136`, `516835904`, `0821283cb` (session rotation
  and bounded malformed-call recovery, adapted to this fork's executor).
- sub2api `a3298b46ebe239fb0720b068a1eb3383955bdac2`:
  `backend/internal/pkg/antigravity/request_transformer.go` and
  `backend/internal/service/antigravity_gateway_compat.go` (always supply a
  function-calling configuration and prefer client functions over incompatible
  built-in search/code execution); malformed calls must not become successful
  empty responses.

Acceptance scenarios:

```gherkin
Scenario: Valid requests across supported entrypoints
  Given valid credentials and an upstream response
  When a streaming or non-streaming Antigravity request is made
  Then the SSE endpoint is used and the answer and terminal event reach the client

Scenario: Mixed client and built-in tools
  Given an agent request with function tools and built-in search or code execution
  When the upstream request is prepared
  Then client functions and their schemas remain and incompatible built-ins are removed
  And an existing explicit function-calling policy is preserved

Scenario: Exhausted upstream session
  Given a request rejected with the per-session input-token ceiling
  When the executor retries
  Then it uses a different session without changing conversation contents
  And retries are bounded and unrelated 400 errors are returned immediately

Scenario: Malformed upstream function call
  Given an upstream MALFORMED_FUNCTION_CALL before visible output
  When the executor re-requests the answer
  Then at most two additional requests use the current rotated session
  And exhausted recovery or malformed calls after visible output produce an error

Scenario: Invalid credentials and upstream failures
  Given missing credentials, a backend error, or a truncated SSE payload
  When a request is executed
  Then the error reaches the client without a successful synthetic completion
```

Verification is local, with an HTTP test server in
`internal/runtime/executor/antigravity_recovery_test.go`. It does not establish
production deployment or acceptance with a real Google account.
