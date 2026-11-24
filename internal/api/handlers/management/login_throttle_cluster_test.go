package management

import (
	"context"
	"database/sql"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster/sharedstate"
	pgredis "github.com/zuohuadong/pgredis-go"
)

// countingThrottleStore counts shared peeks, to show which checks reach the
// shared store.
type countingThrottleStore struct {
	*sharedstate.PGStore
	peeks atomic.Int64
}

func (s *countingThrottleStore) ThrottlePeek(ctx context.Context, bucket string, spec sharedstate.ThrottleSpec, now time.Time) (sharedstate.ThrottleState, error) {
	s.peeks.Add(1)
	return s.PGStore.ThrottlePeek(ctx, bucket, spec, now)
}

// installClusterThrottle needs a real server: the shared state lives in the
// cluster database.
func installClusterThrottle(t *testing.T) (*sql.DB, *countingThrottleStore) {
	t.Helper()
	dsn := os.Getenv("PGREDIS_TEST_DSN")
	if dsn == "" {
		t.Skip("PGREDIS_TEST_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Skipf("postgres unreachable: %v", err)
	}
	store := pgredis.New(db, pgredis.Options{Namespace: "mgmt-throttle-test", TablePrefix: "clirelay"})
	if err := store.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("EnsureSchema: %v", err)
	}
	wrapped := &countingThrottleStore{PGStore: sharedstate.NewPGStore(store)}
	SetClusterThrottleStore(wrapped)
	t.Cleanup(func() {
		SetClusterThrottleStore(nil)
		_ = db.Close()
	})
	return db, wrapped
}

func TestClusterLoginThrottleCountsFailuresFromEveryNode(t *testing.T) {
	installClusterThrottle(t)
	nodeA, nodeB := newTestThrottle(nil), newTestThrottle(nil)
	key := accountThrottleKey(scopeUserAccount, "alice")
	now := time.Now()

	// defaultAccountFailureLimit is 5: three failures on one node and two on the
	// other must arm the block, although neither node saw five.
	for i := 0; i < 3; i++ {
		if d := nodeA.recordFailure(key, now); d.Outcome != outcomeAllow {
			t.Fatalf("failure %d on node a = %+v", i+1, d)
		}
	}
	if d := nodeB.recordFailure(key, now); d.Outcome != outcomeAllow || d.ShortCount != 4 {
		t.Fatalf("fourth failure cluster-wide = %+v", d)
	}
	armed := nodeB.recordFailure(key, now)
	if armed.Outcome != outcomeRateLimited || !armed.NewlyArmed || armed.RetryAfter != time.Minute {
		t.Fatalf("fifth failure cluster-wide must arm the first rung: %+v", armed)
	}
	if d := nodeA.evaluate(key, now.Add(time.Second)); d.Outcome != outcomeRateLimited {
		t.Fatalf("node a must enforce a block armed by node b: %+v", d)
	}
}

func TestClusterLoginThrottleLocalBlockSkipsSharedStore(t *testing.T) {
	_, store := installClusterThrottle(t)
	node := newTestThrottle(nil)
	key := accountThrottleKey(scopeUserAccount, "bob")
	now := time.Now()
	for i := 0; i < defaultAccountFailureLimit; i++ {
		node.recordFailure(key, now)
	}
	before := store.peeks.Load()
	if d := node.evaluate(key, now.Add(time.Second)); d.Outcome == outcomeAllow {
		t.Fatalf("expected a block: %+v", d)
	}
	if store.peeks.Load() != before {
		t.Fatal("a block this node armed itself must be enforced without a shared round trip")
	}
	if d := node.evaluate(accountThrottleKey(scopeUserAccount, "carol"), now); d.Outcome != outcomeAllow || store.peeks.Load() != before+1 {
		t.Fatalf("an unblocked bucket consults the cluster: %+v peeks=%d", d, store.peeks.Load())
	}
}

func TestClusterLoginThrottleSuccessClearsSharedBucket(t *testing.T) {
	installClusterThrottle(t)
	nodeA, nodeB := newTestThrottle(nil), newTestThrottle(nil)
	key := accountThrottleKey(scopeUserAccount, "dave")
	now := time.Now()
	for i := 0; i < 4; i++ {
		nodeB.recordFailure(key, now)
	}
	nodeA.recordSuccess(key)
	deadline := time.Now().Add(3 * time.Second)
	for {
		d := nodeA.recordFailure(key, now)
		if d.ShortCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a success on node a must clear the failures node b recorded, got %+v", d)
		}
		nodeA.recordSuccess(key)
		time.Sleep(10 * time.Millisecond)
	}
}

// TestClusterLoginThrottleFallsBackToLocalThresholds pins the degraded mode:
// without the shared store each node counts on its own, with the full
// thresholds rather than a share of them.
func TestClusterLoginThrottleFallsBackToLocalThresholds(t *testing.T) {
	db, _ := installClusterThrottle(t)
	node := newTestThrottle(nil)
	key := accountThrottleKey(scopeUserAccount, "erin")
	now := time.Now()

	// The database goes away: every shared call fails and the node falls back
	// to its local throttle.
	_ = db.Close()
	var d throttleDecision
	for i := 0; i < defaultAccountFailureLimit-1; i++ {
		if d = node.recordFailure(key, now); d.Outcome != outcomeAllow {
			t.Fatalf("failure %d below the full threshold = %+v", i+1, d)
		}
	}
	if d = node.recordFailure(key, now); d.Outcome == outcomeAllow {
		t.Fatalf("the full local threshold still arms a block: %+v", d)
	}
}

func TestClusterManagementKeyClearIsDeduplicated(t *testing.T) {
	sharedClears.Range(func(k, _ any) bool {
		sharedClears.Delete(k)
		return true
	})
	now := time.Now()
	if !claimSharedClear("management_key|10.0.0.1/32", now) {
		t.Fatal("first clear must go through")
	}
	if claimSharedClear("management_key|10.0.0.1/32", now.Add(time.Second)) {
		t.Fatal("clears of one bucket are spaced out")
	}
	if !claimSharedClear("management_key|10.0.0.1/32", now.Add(sharedClearInterval)) {
		t.Fatal("after the interval the bucket is cleared again")
	}
}
