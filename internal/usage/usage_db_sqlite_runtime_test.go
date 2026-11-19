package usage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/accessfreeze"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// TestInitSQLiteRuntime proves the production SQLite path opens, applies the
// PostgreSQL migration set through the compatibility layer, and serves the
// queries the runtime actually issues (PostgreSQL functions and timestamp
// ordering) without a PostgreSQL server.
func TestInitSQLiteRuntime(t *testing.T) {
	CloseDB()
	dbPath := filepath.Join(t.TempDir(), "data", "usage.db")
	if err := InitSQLiteRuntime(dbPath, config.RequestLogStorageConfig{}, time.UTC); err != nil {
		t.Fatalf("InitSQLiteRuntime() error = %v", err)
	}
	t.Cleanup(CloseDB)

	if got := RuntimeDriver(); got != "sqlite" {
		t.Fatalf("RuntimeDriver() = %q, want sqlite", got)
	}
	if got := GetDBPath(); got != dbPath {
		t.Fatalf("GetDBPath() = %q, want %q", got, dbPath)
	}
	stats, err := GetDatabaseStats()
	if err != nil {
		t.Fatalf("GetDatabaseStats() error = %v", err)
	}
	if stats.Driver != "sqlite" {
		t.Fatalf("DatabaseStats.Driver = %q, want sqlite", stats.Driver)
	}
	if stats.SizeBytes <= 0 {
		t.Fatalf("DatabaseStats.SizeBytes = %d, want > 0", stats.SizeBytes)
	}

	db := RuntimeDB()
	if db == nil {
		t.Fatal("RuntimeDB() is nil after InitSQLiteRuntime")
	}
	ctx := context.Background()

	// Tables the runtime expects come from the PostgreSQL migration list, not
	// from the test-only SQLite bootstrap.
	for _, table := range []string{"identity_fingerprints", "users", "menus", "access_freezes", "ip_access_rules", "runtime_settings"} {
		var name string
		if err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}

	// now() must be a fixed-width UTC timestamp, and comparable with the
	// timestamps the driver binds.
	var nowText string
	if err := db.QueryRowContext(ctx, `SELECT now()`).Scan(&nowText); err != nil {
		t.Fatalf("SELECT now(): %v", err)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000000000Z", nowText); err != nil {
		t.Fatalf("now() = %q is not the fixed-width layout: %v", nowText, err)
	}
	future := time.Now().UTC().Add(time.Hour)
	var newer int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 WHERE ? > now())`, future).Scan(&newer); err != nil {
		t.Fatalf("timestamp comparison: %v", err)
	}
	if newer != 1 {
		t.Fatalf("bound time.Time did not compare greater than now()")
	}

	// A PostgreSQL upsert with a composite conflict target has to work.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO runtime_settings (tenant_id, setting_key, payload) VALUES ('t1', 'k', '{}')
		 ON CONFLICT (tenant_id, setting_key) DO UPDATE SET payload = EXCLUDED.payload`); err != nil {
		t.Fatalf("upsert runtime_settings: %v", err)
	}
}

// TestInitSQLiteRuntimeRejectsForeignSchema guards the upgrade path: a database
// file written by the pre-PostgreSQL builds has tables but no migration
// history, so it must be refused rather than half-migrated.
func TestInitSQLiteRuntimeRejectsForeignSchema(t *testing.T) {
	CloseDB()
	t.Cleanup(CloseDB)
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE request_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp DATETIME NOT NULL)`); err != nil {
		_ = legacy.Close()
		t.Fatalf("create legacy table: %v", err)
	}
	_ = legacy.Close()

	err = InitSQLiteRuntime(dbPath, config.RequestLogStorageConfig{}, time.UTC)
	if err == nil {
		t.Fatal("InitSQLiteRuntime accepted a database with a foreign schema")
	}
	if !strings.Contains(err.Error(), "no migration history") {
		t.Fatalf("unexpected error: %v", err)
	}
	if RuntimeDriver() != "" {
		t.Fatalf("runtime database must stay closed after a rejected schema")
	}
}

// TestSQLiteRuntimeAccessFreeze exercises the PostgreSQL-flavoured queries the
// access-freeze subsystem issues: $N placeholders, a partial unique index as an
// ON CONFLICT target, `::text` casts and the `'-infinity'::timestamptz`
// sentinel. ed60a3be9 disabled this subsystem in SQLite mode; the migration set
// makes the tables real, so it can stay on.
func TestSQLiteRuntimeAccessFreeze(t *testing.T) {
	CloseDB()
	dbPath := filepath.Join(t.TempDir(), "data", "usage.db")
	if err := InitSQLiteRuntime(dbPath, config.RequestLogStorageConfig{}, time.UTC); err != nil {
		t.Fatalf("InitSQLiteRuntime() error = %v", err)
	}
	t.Cleanup(CloseDB)

	store := accessfreeze.NewStore(RuntimeDB())
	ctx := context.Background()
	subject := accessfreeze.Subject{
		TenantID: "00000000-0000-0000-0000-000000000001",
		Kind:     accessfreeze.SubjectAPIKey,
		ID:       "key-under-test",
	}
	strike := accessfreeze.Strike{
		Subject:    subject,
		Provider:   "claude",
		ReasonCode: "content_refusal",
		Model:      "claude-test",
		CreatedAt:  time.Now().UTC(),
	}

	if _, err := store.RecordStrike(ctx, strike); err != nil {
		t.Fatalf("RecordStrike: %v", err)
	}
	freeze, created, err := store.Freeze(ctx, strike, 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if !created || freeze.Subject.TenantID != subject.TenantID {
		t.Fatalf("Freeze returned created=%v subject=%+v", created, freeze.Subject)
	}
	// The partial unique index must make a second standing freeze a no-op.
	if _, createdAgain, err := store.Freeze(ctx, strike, 2, time.Now().UTC()); err != nil {
		t.Fatalf("second Freeze: %v", err)
	} else if createdAgain {
		t.Fatal("a standing freeze must not be duplicated")
	}
	active, err := store.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("ListActive returned %d freezes, want 1", len(active))
	}
	if _, err := store.Release(ctx, subject.TenantID, freeze.ID, "tester", "smoke", time.Now().UTC()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if active, err = store.ListActive(ctx); err != nil {
		t.Fatalf("ListActive after release: %v", err)
	} else if len(active) != 0 {
		t.Fatalf("ListActive after release returned %d freezes, want 0", len(active))
	}
}
