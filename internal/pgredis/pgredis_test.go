package pgredis_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/pgredis"
)

// testDSN is opt-in: the suite needs a real PostgreSQL because every guarantee
// under test (single-statement atomicity, SKIP LOCKED, advisory locks) is a
// property of the server, not of the Go code.
func testDSN(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"PGREDIS_TEST_DSN", "CLIRELAY_TEST_POSTGRES_DSN", "TEST_DATABASE_URL"} {
		if dsn := os.Getenv(name); dsn != "" {
			return dsn
		}
	}
	t.Skip("pgredis: set PGREDIS_TEST_DSN to run the PostgreSQL suite")
	return ""
}

type fixture struct {
	store *pgredis.Store
	db    *sql.DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("pgx", testDSN(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Skipf("pgredis: database unreachable: %v", err)
	}
	// One schema per test run: the tables are shared by every subtest, so the
	// namespace keeps them apart instead.
	store := pgredis.New(db, pgredis.Options{Namespace: uniqueNamespace(t.Name())})
	if err := store.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("ensure schema: %v", err)
	}
	f := &fixture{store: store, db: db}
	t.Cleanup(func() {
		for _, table := range store.Tables() {
			_, _ = db.ExecContext(ctx, "DELETE FROM "+table)
		}
		_ = db.Close()
	})
	return f
}

func uniqueNamespace(name string) string {
	return fmt.Sprintf("test_%s_%d_%d", sanitize(name), time.Now().UnixNano(), os.Getpid())
}

func sanitize(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			out = append(out, r)
		}
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return string(out)
}

type sessionValue struct {
	UserID  int    `json:"userId"`
	Version string `json:"version"`
}

func TestKVRespectsTTLAndConditions(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// Absent reads are not errors.
	found, err := f.store.Get(ctx, "token", &sessionValue{})
	if err != nil || found {
		t.Fatalf("absent get: found=%v err=%v", found, err)
	}

	if err := f.store.Set(ctx, "token", sessionValue{UserID: 1}, 0); err != nil {
		t.Fatalf("set: %v", err)
	}
	var got sessionValue
	if found, err := f.store.Get(ctx, "token", &got); err != nil || !found || got.UserID != 1 {
		t.Fatalf("get after set: found=%v err=%v value=%+v", found, err, got)
	}

	// NX rejects the second write, XX rejects a write to a missing key.
	written, err := f.store.Put(ctx, "token", sessionValue{UserID: 2}, pgredis.KVPutOptions{NX: true})
	if err != nil || written {
		t.Fatalf("nx on existing key: written=%v err=%v", written, err)
	}
	written, err = f.store.Put(ctx, "missing", sessionValue{UserID: 2}, pgredis.KVPutOptions{XX: true})
	if err != nil || written {
		t.Fatalf("xx on missing key: written=%v err=%v", written, err)
	}

	// Compare-and-swap only matches the stored value.
	swapped, err := f.store.CompareAndSwapEqual(ctx, "token", sessionValue{UserID: 9}, sessionValue{UserID: 3}, 0)
	if err != nil || swapped {
		t.Fatalf("cas mismatch: swapped=%v err=%v", swapped, err)
	}
	swapped, err = f.store.CompareAndSwapEqual(ctx, "token", sessionValue{UserID: 1}, sessionValue{UserID: 3}, 0)
	if err != nil || !swapped {
		t.Fatalf("cas match: swapped=%v err=%v", swapped, err)
	}

	// A short TTL hides the value without deleting the row.
	if err := f.store.Set(ctx, "ephemeral", 1, pgredis.TTL(40*time.Millisecond)); err != nil {
		t.Fatalf("set ttl: %v", err)
	}
	if found, _ := f.store.Get(ctx, "ephemeral", nil); !found {
		t.Fatal("value should be visible before its TTL elapses")
	}
	time.Sleep(80 * time.Millisecond)
	if found, _ := f.store.Get(ctx, "ephemeral", nil); found {
		t.Fatal("value should be invisible after its TTL elapses")
	}
	// The row is still there until a sweep collects it.
	swept, err := f.store.SweepKV(ctx, 100)
	if err != nil || swept != 1 {
		t.Fatalf("sweep: swept=%d err=%v", swept, err)
	}
}

func TestCounterSurvivesConcurrency(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	value, err := f.store.CounterAdd(ctx, "hits", 5, 0)
	if err != nil || value != 5 {
		t.Fatalf("add: value=%d err=%v", value, err)
	}
	if _, err := f.store.CounterAdd(ctx, "hits", -2, 0); err != nil {
		t.Fatalf("decrement: %v", err)
	}
	if value, found, err := f.store.CounterGet(ctx, "hits"); err != nil || !found || value != 3 {
		t.Fatalf("get: value=%d found=%v err=%v", value, found, err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.store.CounterAdd(ctx, "hits", 1, 0); err != nil {
				t.Errorf("concurrent add: %v", err)
			}
		}()
	}
	wg.Wait()
	if value, _, _ := f.store.CounterGet(ctx, "hits"); value != 43 {
		t.Fatalf("concurrent increments lost an update: got %d want 43", value)
	}
}

func TestSlotPoolNeverOvercommits(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	const limit = 5
	const attempts = 40
	var taken int64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			reservation, err := f.store.TryReserveSlot(ctx, "subject:1", fmt.Sprintf("req-%d", id), limit, time.Minute)
			if err != nil {
				t.Errorf("reserve: %v", err)
				return
			}
			if reservation.Taken {
				atomic.AddInt64(&taken, 1)
			}
		}(i)
	}
	wg.Wait()
	if taken != limit {
		t.Fatalf("reserved %d slots, want exactly %d", taken, limit)
	}

	// The pool reports the same occupancy through the zset.
	if held, err := f.store.ZCard(ctx, "subject:1"); err != nil || held != limit {
		t.Fatalf("card: held=%d err=%v", held, err)
	}

	// Releasing one frees exactly one slot.
	members, err := f.store.Holders(ctx, "subject:1")
	if err != nil || len(members) == 0 {
		t.Fatalf("holders: %v %v", members, err)
	}
	if released, err := f.store.ReleaseSlot(ctx, "subject:1", members[0].Member); err != nil || !released {
		t.Fatalf("release: released=%v err=%v", released, err)
	}
	if reservation, err := f.store.TryReserveSlot(ctx, "subject:1", "late", limit, time.Minute); err != nil || !reservation.Taken {
		t.Fatalf("reserve after release: %+v err=%v", reservation, err)
	}
}

func TestSlotExpiryReclaimsCapacity(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	if reservation, err := f.store.TryReserveSlot(ctx, "auth:1", "a", 1, 40*time.Millisecond); err != nil || !reservation.Taken {
		t.Fatalf("first reserve: %+v err=%v", reservation, err)
	}
	if reservation, err := f.store.TryReserveSlot(ctx, "auth:1", "b", 1, time.Minute); err != nil || reservation.Taken {
		t.Fatalf("reserve while full: %+v err=%v", reservation, err)
	}
	time.Sleep(90 * time.Millisecond)
	// The expired holder is pruned by the next attempt, not by a background job.
	if reservation, err := f.store.TryReserveSlot(ctx, "auth:1", "b", 1, time.Minute); err != nil || !reservation.Taken {
		t.Fatalf("reserve after expiry: %+v err=%v", reservation, err)
	}
	// Renewing a lost slot must fail so the holder learns it lost it.
	if renewed, err := f.store.RenewSlot(ctx, "auth:1", "a", time.Minute); err != nil || renewed {
		t.Fatalf("renew lost slot: renewed=%v err=%v", renewed, err)
	}
}

func TestRateLimitFixedWindow(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	spec := pgredis.RateLimitSpec{Window: time.Minute, Limit: 3}

	for i := 1; i <= 3; i++ {
		result, err := f.store.RateLimitHit(ctx, "user:1", spec)
		if err != nil {
			t.Fatalf("hit %d: %v", i, err)
		}
		if !result.Allowed || result.Used != int64(i) {
			t.Fatalf("hit %d: %+v", i, result)
		}
	}
	denied, err := f.store.RateLimitHit(ctx, "user:1", spec)
	if err != nil {
		t.Fatalf("over-limit hit: %v", err)
	}
	if denied.Allowed || denied.Remaining != 0 || denied.RetryAfter <= 0 {
		t.Fatalf("over-limit result: %+v", denied)
	}

	peek, err := f.store.RateLimitPeek(ctx, "user:1", spec)
	if err != nil || peek.Used != 4 {
		t.Fatalf("peek: %+v err=%v", peek, err)
	}
	// A reward returns budget: two units back leaves room for exactly one hit.
	rewarded, err := f.store.RateLimitReward(ctx, "user:1", 2, spec)
	if err != nil || rewarded.Used != 2 {
		t.Fatalf("reward: %+v err=%v", rewarded, err)
	}
	if result, err := f.store.RateLimitHit(ctx, "user:1", spec); err != nil || !result.Allowed || result.Used != 3 {
		t.Fatalf("hit after reward: %+v err=%v", result, err)
	}
	if result, err := f.store.RateLimitHit(ctx, "user:1", spec); err != nil || result.Allowed {
		t.Fatalf("budget should be spent again: %+v err=%v", result, err)
	}
}

func TestQueueClaimAckAndRetry(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	first, err := f.store.Enqueue(ctx, "jobs", "webhook", []byte(`{"id":1}`), 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := f.store.Enqueue(ctx, "jobs", "webhook", []byte(`{"id":2}`), 0); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}
	if depth, err := f.store.QueueDepth(ctx, "jobs"); err != nil || depth != 2 {
		t.Fatalf("depth: %d err=%v", depth, err)
	}

	// Two workers get different jobs: SKIP LOCKED keeps them from colliding.
	a, ok, err := f.store.Dequeue(ctx, "jobs", "worker-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("dequeue a: ok=%v err=%v", ok, err)
	}
	b, ok, err := f.store.Dequeue(ctx, "jobs", "worker-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("dequeue b: ok=%v err=%v", ok, err)
	}
	if a.ID == b.ID {
		t.Fatalf("both workers claimed job %d", a.ID)
	}
	if _, ok, err := f.store.Dequeue(ctx, "jobs", "worker-c", time.Minute); err != nil || ok {
		t.Fatalf("third dequeue should find nothing: ok=%v err=%v", ok, err)
	}

	// Ack removes one; retry with a backoff hides the other until it is due.
	if removed, err := f.store.Ack(ctx, first.ID); err != nil || !removed {
		t.Fatalf("ack: removed=%v err=%v", removed, err)
	}
	other := b.ID
	if other == first.ID {
		other = a.ID
	}
	if requeued, err := f.store.Retry(ctx, other, time.Hour, 5); err != nil || !requeued {
		t.Fatalf("retry: requeued=%v err=%v", requeued, err)
	}
	if _, ok, err := f.store.Dequeue(ctx, "jobs", "worker-a", time.Minute); err != nil || ok {
		t.Fatalf("delayed job became visible early: ok=%v err=%v", ok, err)
	}
}

func TestQueueVisibilityTimeoutReclaimsCrashedWorker(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	job, err := f.store.Enqueue(ctx, "jobs", "webhook", []byte(`{}`), 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, ok, err := f.store.Dequeue(ctx, "jobs", "worker-a", 40*time.Millisecond); err != nil || !ok {
		t.Fatalf("dequeue: ok=%v err=%v", ok, err)
	}
	if _, ok, err := f.store.Dequeue(ctx, "jobs", "worker-b", time.Minute); err != nil || ok {
		t.Fatalf("job should be invisible while leased: ok=%v err=%v", ok, err)
	}
	time.Sleep(90 * time.Millisecond)
	reclaimed, ok, err := f.store.Dequeue(ctx, "jobs", "worker-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("job should return after its visibility timeout: ok=%v err=%v", ok, err)
	}
	if reclaimed.ID != job.ID || reclaimed.Attempts != 2 {
		t.Fatalf("reclaim: id=%d attempts=%d", reclaimed.ID, reclaimed.Attempts)
	}
}

func TestAdvisoryLockIsExclusive(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.store.WithAdvisoryLock(ctx, "flush", false, func(context.Context, pgredis.Querier) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	// The second caller must not wait and must not run its body.
	ran := false
	err := f.store.WithAdvisoryLock(ctx, "flush", false, func(context.Context, pgredis.Querier) error {
		ran = true
		return nil
	})
	if !errors.Is(err, pgredis.ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy, got %v", err)
	}
	if ran {
		t.Fatal("lock body ran while the lock was held")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("lock holder: %v", err)
	}
	// Once released, the same name is free again.
	if err := f.store.WithAdvisoryLock(ctx, "flush", false, func(context.Context, pgredis.Querier) error {
		return nil
	}); err != nil {
		t.Fatalf("lock after release: %v", err)
	}
}

func TestLeaseLockProvesOwnership(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	lock, ok, err := f.store.TryLeaseLock(ctx, "refresh:auth1", "node-a", pgredis.TTL(time.Minute))
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	if _, ok, err := f.store.TryLeaseLock(ctx, "refresh:auth1", "node-b", pgredis.TTL(time.Minute)); err != nil || ok {
		t.Fatalf("second owner acquired the lease: ok=%v err=%v", ok, err)
	}
	if renewed, err := f.store.RenewLease(ctx, lock, pgredis.TTL(time.Minute)); err != nil || !renewed {
		t.Fatalf("renew: renewed=%v err=%v", renewed, err)
	}
	// A different owner cannot release the lease.
	if unlocked, err := f.store.Unlock(ctx, pgredis.LeaseLock{}); err != nil || unlocked {
		t.Fatalf("empty lease unlocked something: %v %v", unlocked, err)
	}
	if unlocked, err := f.store.Unlock(ctx, lock); err != nil || !unlocked {
		t.Fatalf("unlock: unlocked=%v err=%v", unlocked, err)
	}
}
