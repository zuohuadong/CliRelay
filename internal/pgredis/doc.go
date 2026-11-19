// Package pgredis replaces the Redis primitives this project relies on with
// PostgreSQL, following the shape of the TypeScript @postgresx/noredis
// toolkit (formerly published as vibeunion/pgredis).
//
// It is not a Redis client and it does not speak the Redis protocol. Every
// method is a plain SQL statement, so the same database that holds request
// logs, configuration and cluster membership also holds the ephemeral state a
// Redis instance would have kept:
//
//   - KV values with TTL, NX/XX and compare-and-swap (kv.go)
//   - integer counters (counter.go)
//   - sorted sets, including an expiring-member pool for concurrency slots
//     (zset.go)
//   - fixed-window, sliding-window and token-bucket rate limits
//     (ratelimit.go)
//   - a durable job queue with SKIP LOCKED (queue.go)
//   - session advisory locks (lock.go)
//
// Design rules, all copied from pgredis because they are what makes this
// workable:
//
//  1. One statement per operation. A statement that reads and writes its own
//     rows (INSERT ... ON CONFLICT ... RETURNING, DELETE ... RETURNING,
//     UPDATE ... FROM (CTE)) is atomic on its own, so no operation needs a
//     multi-statement transaction to be safe against another node.
//  2. Time comes from the database (now()), never from the client clock, so
//     nodes never disagree about expiry.
//  3. Cache, counter and pool tables are UNLOGGED by default: no WAL, no
//     fsync on commit. They survive a clean restart and are truncated by crash
//     recovery, which is exactly the durability level of the Redis data they
//     replace. Use Options.Logged to opt into WAL.
//  4. Expiry is lazy on read and swept in batches on write. An expired row is
//     invisible to every reader; no background job is required for
//     correctness, only for reclaiming space.
//  5. Absent state is a normal answer, not an error. Callers decide whether an
//     unavailable store degrades to node-local behaviour.
//
// The cost of this trade is measured, not guessed: pgredis's own benchmark
// puts a PostgreSQL counter at roughly 6k-11k ops/s against Redis's 41k, with
// p50 latency of 1.1-2.1ms against 0.37ms. Reads served from an in-process L1
// are faster than Redis (30-40x) but plain reads are 2-5x slower. Anything on
// the per-request path must therefore batch or pre-deduct locally; see
// RateLimit for the token-bucket variant that does.
package pgredis
