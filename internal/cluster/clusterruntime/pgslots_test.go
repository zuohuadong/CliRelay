package clusterruntime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	pgredis "github.com/zuohuadong/pgredis-go"
)

// The slot adapter only has a guarantee worth testing against a real database:
// the "one slot left" decision is a single SQL statement, and SKIP LOCKED-style
// behaviour cannot be faked in memory.
func pgSlotTestStore(t *testing.T) *pgredis.Store {
	t.Helper()
	dsn := os.Getenv("PGREDIS_TEST_DSN")
	if dsn == "" {
		t.Skip("clusterruntime: set PGREDIS_TEST_DSN to exercise the PostgreSQL slot adapter")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Skipf("clusterruntime: database unreachable: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := pgredis.New(db, pgredis.Options{
		Namespace: fmt.Sprintf("clusterruntime_%d", time.Now().UnixNano()),
	})
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return store
}

func TestPGSlotAdapterEnforcesOneLimitAcrossNodes(t *testing.T) {
	store := pgSlotTestStore(t)
	oneNode := func() *cluster.Coordinator { return nil }

	nodeA := newPGSlotAdapter(store, oneNode, "node-a")
	nodeB := newPGSlotAdapter(store, oneNode, "node-b")
	defer nodeA.Close()
	defer nodeB.Close()

	if !nodeA.Shared() || !nodeB.Shared() {
		t.Fatal("both adapters should report the pool as usable")
	}
	if got := nodeA.NodeShare(5); got != 5 {
		t.Fatalf("NodeShare on one node = %d, want 5", got)
	}

	// A limit of two is shared by the two processes, not applied twice.
	if ok, err := nodeA.Acquire("auth-1", 2); err != nil || !ok {
		t.Fatalf("node A first slot: ok=%v err=%v", ok, err)
	}
	if ok, err := nodeB.Acquire("auth-1", 2); err != nil || !ok {
		t.Fatalf("node B first slot: ok=%v err=%v", ok, err)
	}
	if ok, err := nodeA.Acquire("auth-1", 2); err != nil || ok {
		t.Fatalf("third slot should be refused: ok=%v err=%v", ok, err)
	}

	// Releasing on one node returns capacity to the other, asynchronously.
	nodeA.Release("auth-1")
	deadline := time.Now().Add(3 * time.Second)
	for {
		ok, err := nodeB.Acquire("auth-1", 2)
		if err != nil {
			t.Fatalf("acquire after release: %v", err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("released slot never became available")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A different account has its own pool.
	if ok, err := nodeA.Acquire("auth-2", 1); err != nil || !ok {
		t.Fatalf("separate account: ok=%v err=%v", ok, err)
	}
}

func TestPGSlotAdapterExpiresCrashedHolder(t *testing.T) {
	store := pgSlotTestStore(t)
	oneNode := func() *cluster.Coordinator { return nil }

	crashed := newPGSlotAdapter(store, oneNode, "node-dead")
	crashed.keeper.retention = 50 * time.Millisecond
	defer crashed.Close()
	survivor := newPGSlotAdapter(store, oneNode, "node-live")
	defer survivor.Close()

	if ok, err := crashed.Acquire("auth-1", 1); err != nil || !ok {
		t.Fatalf("crashed node acquire: ok=%v err=%v", ok, err)
	}
	if ok, err := survivor.Acquire("auth-1", 1); err != nil || ok {
		t.Fatalf("survivor should be blocked: ok=%v err=%v", ok, err)
	}
	time.Sleep(90 * time.Millisecond)
	if ok, err := survivor.Acquire("auth-1", 1); err != nil || !ok {
		t.Fatalf("survivor should take the expired slot: ok=%v err=%v", ok, err)
	}
}
