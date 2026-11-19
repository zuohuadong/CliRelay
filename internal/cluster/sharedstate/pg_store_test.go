package sharedstate

import (
	"context"
	"database/sql"
	"os"
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
