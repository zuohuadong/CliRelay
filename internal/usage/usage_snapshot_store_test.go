package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresSnapshotStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("PGREDIS_TEST_DSN")
	if dsn == "" {
		t.Skip("PGREDIS_TEST_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}

	if err := InitPostgresSnapshot(db); err != nil {
		t.Fatalf("InitPostgresSnapshot: %v", err)
	}
	t.Cleanup(func() { usageSnapshotStore = nil })

	if usageSnapshotStore.Name() != "PostgreSQL" {
		t.Fatalf("store name = %q", usageSnapshotStore.Name())
	}
	payload := []byte(`{"total_requests":7}`)
	if err := usageSnapshotStore.Save(ctx, payload); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, found, err := usageSnapshotStore.Load(ctx)
	if err != nil || !found {
		t.Fatalf("Load: found=%v err=%v", found, err)
	}
	// jsonb normalises whitespace, so compare the decoded value.
	var want, have map[string]int
	if err := json.Unmarshal(payload, &want); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if err := json.Unmarshal(got, &have); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if have["total_requests"] != want["total_requests"] {
		t.Fatalf("Load = %s, want %s", got, payload)
	}
}

func TestPostgresSnapshotStoreMissingKey(t *testing.T) {
	dsn := os.Getenv("PGREDIS_TEST_DSN")
	if dsn == "" {
		t.Skip("PGREDIS_TEST_DSN is not set")
	}
	if err := InitPostgresSnapshot(nil); err == nil {
		t.Fatal("InitPostgresSnapshot(nil) must fail")
	}
}
