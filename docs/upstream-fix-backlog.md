# Upstream fix backlog (v7/v8 → this line)

This file records what we decided to take from the upstream `router-for-me/CLIProxyAPI`
(v8) line, why, and what we deliberately left alone. It exists so the next person
does not have to re-derive it from 348 fix commits.

## Method

```sh
git fetch upstream main
git log --oneline --no-merges upstream/main --since="4 weeks ago" -i --grep="fix" | wc -l      # 348
git log --oneline --no-merges upstream/main --since="4 weeks ago" -i --grep="fix" \
  | grep -iE "xai|grok|codex|reasoning|encrypted|ciphertext|signature|retry|overload|capacity|stream|incomplete|terminal|compact|thinking"
```

Each candidate is judged on two things only:

1. **Does it explain a failure we can see in production?** (see the taxonomy below)
2. **Can it be taken as a self-contained change?** A fix that depends on v8's
   `ojson` document model, `homekv`, or the `signature` package is a rewrite here,
   not a port, and is listed as such.

Porting the whole v8 line is not on the table: `fork/main..upstream/main` is
5352 files, +953k/−650k lines, and v8 reintroduces Redis (`go-redis`,
`internal/redisqueue`, `internal/home`) which this line deliberately removed.

## Production failure taxonomy (24h snapshot, 2026-11-24)

| model | requests | failed attempts | note |
| --- | --- | --- | --- |
| gpt-6.2-sol | 11856 | 2368 (20.0%) | ChatGPT fleet overload (`server_is_overloaded`) |
| gpt-6.1-sol | 2052 | 395 (19.2%) | same |
| gpt-6.1-terra | 611 | 207 (33.9%) | same |
| grok-4.7 | 63 | 32 (50.8%) | reasoning replay (`content: null`) |

`failed` counts attempts, not client-visible failures: a retried-then-successful
turn leaves a failed attempt row behind. It is still the right ranking signal.

## Already fixed here

| Commit | What | Evidence |
| --- | --- | --- |
| `1881f47c0` | Codex `response.done` accepted as terminal | upstream had it (`4203cfa1e`) |
| `323909ea8` | failed streams end with `response.failed` | client no longer reports "stream closed before response.completed" |
| `4f82a88f8` | Grok replayed reasoning keeps `content: null` out of the item | the exact failing body now returns 200, 3/3 |
| `d41b6c9ea` | overload → 503 + the event's own retry-after, bounded credential park, `Retry-After` to the client | `server_is_overloaded` events carry `headers.retry-after` (~370s) |
| `cd5b3c751` | `compaction*` items are replayed byte for byte | — |
| `73e19f15b` | `clirelay-replay` reproduces a recorded request | turns a flaky report into a settled reproduction |

## Backlog, in the order worth doing

| Upstream | What it fixes | This line | Outcome |
| --- | --- | --- | --- |
| `ae3d80f70` | antigravity: honour retry hints on capacity errors | 503 capacity answers now carry the wait | **Done** `16c11f94f` |
| `84b838e1c` | honor HTTP `Retry-After` fallback on 429/503 | `parseRetryDelayWithHeaders`, body first then header | **Done** `16c11f94f` |
| `c71aa824e` | Claude: keep adjacent same-response thinking blocks | ids give the origin, trailing run collapses otherwise | **Done** `698a58311` |
| `d2b7dbbdb` | codex: default missing Claude tool input arguments | omitted `input` becomes `{}` | **Done** `18c73ed32` |
| `85225381d` | antigravity: isolate signature cache by credential | **Blocked**: scoping needs the credential on both sides, but request translators here are `func(model, body, stream)` with no context. Needs a translator-registry change first. |
| `892fc4864` | harden SSE JSON reassembly + mid-stream error bookkeeping | **N/A**: patches v8's `helps/sse_json_assembler.go` and `antigravity_executor_stream.go`; this line has no such modules. Revisit only with evidence of truncated frames. |
| `aeed97157` | gemini: suppress pending filter frames on stream errors | **N/A**: v8 buffers policy text in `gemini_executor.go`; this line has no pending-frame buffer. |
| `f5ef0c0f8` | auth: preserve stream bootstrap attempt identity | **N/A**: `conductor_stream.go` and `streamBootstrapError` do not exist here. |
| `538f96110` | auth: cancel abandoned stream attempts before draining | **N/A**: same v8-only file; this line settles attempts through `wrapStreamResult`. |
| `679166b87` | xai: evict idle websocket ID states | **N/A**: xAI chat here is HTTP only. |

## Deliberately not ported

- **`internal/signature` (23 files).** It is the v8 answer to foreign reasoning
  ciphertext, but it is a cross-cutting subsystem with its own provider taxonomy.
  This line already has the equivalents where they matter: `gAAAA` filtering for
  the Codex backend (`codex_reasoning_ciphertext.go`), a Claude envelope check
  (`claude_openai-responses_reasoning.go`), and, as of `4f82a88f8`, Grok's
  transport shape. Take individual validators only if a real refusal appears.
- **`xai reasoning replay cache` (`internal/cache`, `internal/home`).** It repairs
  stateless replay continuity; xAI traffic here is ~63 requests/day, so it does
  not pay for a KV-backed cache yet. Revisit if grok volume grows 100×.
- **Codex websocket dirty-disconnect replay (`29a9fb229`).** This line already
  retries a lost send/first event and falls back to a full request, then to HTTP
  (`codex_incremental_transport.go`). Six hours of production logs after
  `84be85d1b` contain **zero** "closed without a terminal event" warnings, so
  there is nothing to fix. Watch that counter; if it starts moving, port it.
- **Whole-line switch to v8.** Not a fix, a migration: Redis returns, and every
  business customisation (billing, cluster state, sticky affinity, date-tagged
  releases) is redone.

## Reproducing a failure

```sh
# on the server, with the image's tool
docker exec -i cli-proxy-api /CLIProxyAPI/clirelay-replay \
  -dsn "$CLIRELAY_POSTGRES_DSN" -log-id 3419814 -api-key "$KEY" -repeat 3

# locally, from a body dumped out of the database
go run ./cmd/clirelay-replay -in body.json -base https://cliapi.example -api-key "$KEY"
```

It prints every event, the upstream error text, whether the stream ended on a
terminal event, and the stored upstream log for comparison. Exit status is
non-zero when an attempt ended without a terminal event, so it can gate a fix.
## "Selected model is at capacity. Please try a different model."

That sentence is not the upstream's message — it is Codex's own wording for one
error code. Codex classifies a Responses failure against a closed set of codes
and treats `server_is_overloaded` and `slow_down` as **fatal**: it prints that
line and ends the session instead of backing off. Codes outside the set, such as
`server_error`, go through the client's built-in retry with delay.

ChatGPT reports its own overload as `response.failed` with
`response.error.code = server_is_overloaded`, and the relay used to forward that
verbatim, so every capacity refusal killed the turn even after the retries were
spent. `rate_limit_exceeded` is different: the client parses it for its own
delay, so it must not be rewritten.

Fix (`16833ef0f`): keep the message, rewrite the code to `server_error` whenever a
capacity refusal has to reach the client — streamed events, the terminal failure
the handler synthesizes, non-streaming error bodies, and the websocket transport.
Both references agree: sub2api documents the same rule for its gateway
(`sanitizeOpenAICapacityShedErrorCodeForClient`), and the v8 line classifies the
same two codes as its capacity-shed signal.
