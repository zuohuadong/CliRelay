package sqlitecompat_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/storage/postgres"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/storage/sqlitecompat"
)

func TestApplyMigrationsRuntimeSchema(t *testing.T) {
	db := openRuntime(t)
	ctx := context.Background()

	for _, table := range []string{
		"request_logs",
		"request_log_content",
		"api_keys",
		"api_key_permission_profiles",
		"users",
		"user_sessions",
		"user_session_tokens",
		"roles",
		"audit_logs",
		"menus",
		"tenants",
		"end_users",
		"ip_access_rules",
		"access_freezes",
		"runtime_settings",
		"model_configs",
		"identity_fingerprints",
		"cluster_nodes",
	} {
		var name string
		err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing after migrations: %v", table, err)
		}
	}

	// A re-run must be a no-op: migrations are recorded by version.
	var before int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&before); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if err := sqlitecompat.ApplyMigrations(ctx, db, sqliteMigrations()); err != nil {
		t.Fatalf("re-apply migrations: %v", err)
	}
	var after int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&after); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if before != after {
		t.Fatalf("schema_migrations grew from %d to %d on re-apply", before, after)
	}
}

func TestApplyMigrationsCompositeKeysBecomeUniqueIndexes(t *testing.T) {
	db := openRuntime(t)
	ctx := context.Background()

	for _, index := range []string{"model_pricing_pkey", "runtime_settings_pkey", "auth_file_quota_snapshots_pkey"} {
		var name string
		err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&name)
		if err != nil {
			t.Fatalf("index %s missing after migrations: %v", index, err)
		}
	}

	// ON CONFLICT (tenant_id, key) needs the composite unique index to exist.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO runtime_settings (tenant_id, setting_key, payload) VALUES ('t1', 'k', '{}')`); err != nil {
		t.Fatalf("insert runtime_settings: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO runtime_settings (tenant_id, setting_key, payload) VALUES ('t1', 'k', '{"a":1}')
		 ON CONFLICT (tenant_id, setting_key) DO UPDATE SET payload = EXCLUDED.payload`); err != nil {
		t.Fatalf("upsert runtime_settings: %v", err)
	}
}

func openRuntime(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := sql.Open(sqlitecompat.DriverName, path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), "PRAGMA busy_timeout = 5000"); err != nil {
		t.Fatalf("set busy_timeout: %v", err)
	}
	if err := sqlitecompat.ApplyMigrations(context.Background(), db, sqliteMigrations()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

func sqliteMigrations() []sqlitecompat.Migration {
	runtimeMigrations := postgres.RuntimeMigrations()
	migrations := make([]sqlitecompat.Migration, 0, len(runtimeMigrations))
	for _, migration := range runtimeMigrations {
		migrations = append(migrations, sqlitecompat.Migration{Version: migration.Version, SQL: migration.SQL})
	}
	return migrations
}
