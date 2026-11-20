package clusterruntime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster/sharedstate"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	pgredis "github.com/zuohuadong/pgredis-go"
)

// pgAffinityTestStore builds two stores that share one database and namespace,
// which is what two nodes of one cluster look like.
func pgAffinityTestStore(t *testing.T) (*pgredis.Store, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("PGREDIS_TEST_DSN")
	if dsn == "" {
		t.Skip("clusterruntime: set PGREDIS_TEST_DSN to exercise the PostgreSQL affinity store")
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
		Namespace:   fmt.Sprintf("affinity_%d", time.Now().UnixNano()),
		TablePrefix: "clirelay_affinity",
	})
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return store, db
}

func TestSessionAffinityIsSharedThroughPostgres(t *testing.T) {
	store, db := pgAffinityTestStore(t)
	a := pgAffinityAdapter{store: sharedstate.NewPGStore(store)}
	b := pgAffinityAdapter{store: sharedstate.NewPGStore(store)}

	ref := coreauth.AffinityAccountRef("codex-someone@example.com.json")
	key := "tenant|codex|openai|default|sess-" + time.Now().Format("150405.000000")

	bound, err := a.Bind(context.Background(), key, ref, time.Hour)
	if err != nil || bound.AccountRef != ref {
		t.Fatalf("bind = %+v %v", bound, err)
	}
	seen, err := b.Lookup(context.Background(), key, time.Hour)
	if err != nil || !seen.Found || seen.AccountRef != ref || seen.Served != 1 {
		t.Fatalf("lookup from the other node = %+v %v", seen, err)
	}

	// Session keys are hashed in the database: the raw session id must not
	// appear in a key name an operator can browse.
	var readable int
	if err := db.QueryRow(`SELECT count(*) FROM clirelay_affinity_kv
	                        WHERE key LIKE $1 OR key LIKE '%example.com%'`,
		"%sess-%").Scan(&readable); err != nil {
		t.Fatalf("inspect keys: %v", err)
	}
	if readable != 0 {
		t.Fatalf("session keys must be hashed, found %d readable rows", readable)
	}
	if !strings.Contains(key, "sess-") {
		t.Fatal("test key must carry the readable discriminator")
	}

	if err := b.Release(context.Background(), key, ref); err != nil {
		t.Fatalf("release: %v", err)
	}
	if gone, err := a.Lookup(context.Background(), key, time.Hour); err != nil || gone.Found {
		t.Fatalf("released binding still found: %+v %v", gone, err)
	}
	if !strings.Contains(key, "sess-") {
		t.Fatal("test key must contain the discriminator")
	}
}
