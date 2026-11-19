package usage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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
