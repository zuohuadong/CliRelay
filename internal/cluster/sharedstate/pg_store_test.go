package sharedstate

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	pgredis "github.com/zuohuadong/pgredis-go"
)

// pgStoreTestStore needs a real server: the guarantee is the single statement.
func pgStoreTestStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("PGREDIS_TEST_DSN")
	if dsn == "" {
		t.Skip("PGREDIS_TEST_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}
	store := pgredis.New(db, pgredis.Options{Namespace: "sharedstate-test", TablePrefix: "clirelay"})
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return NewPGStore(store)
}

func TestPGStoreMintIDFirstWriterWins(t *testing.T) {
	store := pgStoreTestStore(t)
	ctx := context.Background()
	key := "conversation-" + time.Now().Format("150405.000000")

	first, err := store.MintID(ctx, "upstream", key, "candidate-a", time.Minute, false)
	if err != nil {
		t.Fatalf("MintID first: %v", err)
	}
	if !first.Minted || first.ID != "candidate-a" {
		t.Fatalf("first writer must win: %+v", first)
	}
	if first.TTL <= 0 || first.TTL > time.Minute {
		t.Fatalf("TTL = %v, want (0, 1m]", first.TTL)
	}

	second, err := store.MintID(ctx, "upstream", key, "candidate-b", time.Minute, false)
	if err != nil {
		t.Fatalf("MintID second: %v", err)
	}
	if second.Minted || second.ID != "candidate-a" {
		t.Fatalf("second writer must read the winner: %+v", second)
	}

	// Another namespace is independent.
	other, err := store.MintID(ctx, "other", key, "candidate-c", time.Minute, false)
	if err != nil {
		t.Fatalf("MintID other namespace: %v", err)
	}
	if !other.Minted || other.ID != "candidate-c" {
		t.Fatalf("namespaces must not collide: %+v", other)
	}
}

func TestPGStoreMintIDSlidingTTL(t *testing.T) {
	store := pgStoreTestStore(t)
	ctx := context.Background()
	key := "sliding-" + time.Now().Format("150405.000000")

	if _, err := store.MintID(ctx, "upstream", key, "candidate", 2*time.Second, false); err != nil {
		t.Fatalf("MintID seed: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	read, err := store.MintID(ctx, "upstream", key, "candidate", 2*time.Second, true)
	if err != nil {
		t.Fatalf("MintID sliding: %v", err)
	}
	if read.TTL < 1500*time.Millisecond {
		t.Fatalf("sliding read must extend the TTL, got %v", read.TTL)
	}
}

func TestPGStoreUnavailableWithoutStore(t *testing.T) {
	store := NewPGStore(nil)
	if store.Available() {
		t.Fatal("a nil pgredis store must be unavailable")
	}
	if _, err := store.MintID(context.Background(), "ns", "k", "c", time.Minute, false); err == nil {
		t.Fatal("MintID must fail when the store is unavailable")
	}
}

func TestPGStoreAffinityBindAndServe(t *testing.T) {
	store := pgStoreTestStore(t)
	ctx := context.Background()
	key := "session-" + time.Now().Format("150405.000000")
	ttl := time.Minute

	if binding, err := store.AffinityLookup(ctx, key, ttl); err != nil {
		t.Fatalf("lookup before bind: %v", err)
	} else if binding.Found {
		t.Fatalf("lookup before bind reported a binding: %+v", binding)
	}

	first, err := store.AffinityBind(ctx, key, "acct-a", ttl)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if !first.Created || first.Account != "acct-a" || first.Served != 0 {
		t.Fatalf("bind must create with Served=0: %+v", first)
	}

	// A second bind run must not steal the binding: it counts the request.
	second, err := store.AffinityBind(ctx, key, "acct-b", ttl)
	if err != nil {
		t.Fatalf("bind again: %v", err)
	}
	if second.Created || second.Account != "acct-a" || second.Served != 1 {
		t.Fatalf("second bind must serve the existing binding: %+v", second)
	}

	lookup, err := store.AffinityLookup(ctx, key, ttl)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !lookup.Found || lookup.Account != "acct-a" || lookup.Served != 2 {
		t.Fatalf("lookup must count the request: %+v", lookup)
	}

	// Releasing with the wrong account leaves the binding alone.
	if err := store.AffinityRelease(ctx, key, "acct-b"); err != nil {
		t.Fatalf("release wrong account: %v", err)
	}
	if still, err := store.AffinityLookup(ctx, key, ttl); err != nil {
		t.Fatalf("lookup after wrong release: %v", err)
	} else if !still.Found {
		t.Fatal("release with the wrong account must not delete the binding")
	}
	if err := store.AffinityRelease(ctx, key, "acct-a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if gone, err := store.AffinityLookup(ctx, key, ttl); err != nil {
		t.Fatalf("lookup after release: %v", err)
	} else if gone.Found {
		t.Fatalf("release must delete the binding: %+v", gone)
	}
}

func TestPGStoreAffinityCountsConcurrentRequests(t *testing.T) {
	store := pgStoreTestStore(t)
	ctx := context.Background()
	key := "concurrent-" + time.Now().Format("150405.000000")
	ttl := time.Minute

	if _, err := store.AffinityBind(ctx, key, "acct-a", ttl); err != nil {
		t.Fatalf("bind: %v", err)
	}

	const requesters = 8
	served := make(chan int, requesters)
	errs := make(chan error, requesters)
	var wg sync.WaitGroup
	for i := 0; i < requesters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			binding, err := store.AffinityLookup(ctx, key, ttl)
			if err != nil {
				errs <- err
				return
			}
			served <- binding.Served
		}()
	}
	wg.Wait()
	close(errs)
	close(served)
	for err := range errs {
		t.Fatalf("concurrent lookup: %v", err)
	}
	seen := make(map[int]bool, requesters)
	for value := range served {
		seen[value] = true
	}
	// Every request must have been counted exactly once: the set of returned
	// counts is 1..requesters with no duplicates or gaps.
	if len(seen) != requesters {
		t.Fatalf("counts seen = %v, want %d distinct values", seen, requesters)
	}
	for i := 1; i <= requesters; i++ {
		if !seen[i] {
			t.Fatalf("count %d was never handed out: %v", i, seen)
		}
	}
	if final, err := store.AffinityLookup(ctx, key, ttl); err != nil {
		t.Fatalf("final lookup: %v", err)
	} else if final.Served != requesters+1 {
		t.Fatalf("final Served = %d, want %d", final.Served, requesters+1)
	}
}
