package ipaccess

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster/sharedstate"
	pgredis "github.com/zuohuadong/pgredis-go"
)

// installClusterAutoBan needs a real server: the shared counters live in the
// cluster database.
func installClusterAutoBan(t *testing.T) *sql.DB {
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
	store := pgredis.New(db, pgredis.Options{Namespace: "ipaccess-autoban-test", TablePrefix: "clirelay"})
	if err := store.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("EnsureSchema: %v", err)
	}
	SetClusterAutoBanStore(sharedstate.NewPGStore(store))
	t.Cleanup(func() {
		SetClusterAutoBanStore(nil)
		_ = db.Close()
	})
	return db
}

func TestClusterAutoBanCountsFailuresFromEveryNode(t *testing.T) {
	installClusterAutoBan(t)
	policy := DefaultPolicy()
	policy.AutoBan.FailureThreshold = 3
	nodeA, nodeB := testRegistry(t, policy), testRegistry(t, policy)
	source := trustedAddress("203.0.113.40")

	for i := 0; i < 2; i++ {
		if out := nodeA.AutoBan().RecordFailure(context.Background(), source, "test"); out.Triggered {
			t.Fatalf("failure %d triggered early", i+1)
		}
	}
	crossing := nodeB.AutoBan().RecordFailure(context.Background(), source, "test")
	if !crossing.Triggered || crossing.Failures != 3 || crossing.CIDR != "203.0.113.40/32" {
		t.Fatalf("the third failure cluster-wide must trigger on whichever node sees it: %+v", crossing)
	}
	// The ban is known everywhere: node a must not trigger a second time.
	for i := 0; i < 3; i++ {
		if out := nodeA.AutoBan().RecordFailure(context.Background(), source, "test"); out.Triggered {
			t.Fatalf("node a re-triggered a ban node b already decided: %+v", out)
		}
	}
}

func TestClusterAutoBanFallsBackToLocalCounting(t *testing.T) {
	db := installClusterAutoBan(t)
	policy := DefaultPolicy()
	policy.AutoBan.FailureThreshold = 3
	node := testRegistry(t, policy)
	source := trustedAddress("203.0.113.41")

	_ = db.Close()
	var out AutoBanOutcome
	for i := 0; i < 3; i++ {
		out = node.AutoBan().RecordFailure(context.Background(), source, "test")
	}
	if !out.Triggered || out.Failures != 3 {
		t.Fatalf("without the shared store the node counts on its own with the full threshold: %+v", out)
	}
}
