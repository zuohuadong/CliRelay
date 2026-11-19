package usage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/storage/postgres"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/storage/sqlitecompat"
	log "github.com/sirupsen/logrus"
)

// InitSQLiteRuntime opens the embedded runtime database for a deployment that
// has no PostgreSQL DSN configured.
//
// Unlike InitDB (kept for tests), this path is a production runtime: it applies
// the same migration list PostgreSQL uses, translated to SQLite by
// internal/storage/sqlitecompat, so a single-node install keeps every table the
// runtime expects without a second schema definition to maintain.
func InitSQLiteRuntime(dbPath string, storageCfg config.RequestLogStorageConfig, loc *time.Location) error {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return fmt.Errorf("usage: sqlite database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("usage: create sqlite directory: %w", err)
	}

	usageDBLifecycleMu.Lock()
	defer usageDBLifecycleMu.Unlock()
	usageWriteLease.Lock()
	defer usageWriteLease.Unlock()

	usageDBMu.Lock()
	defer usageDBMu.Unlock()

	if usageDB != nil {
		return nil
	}
	if loc == nil {
		loc = time.Local
	}

	db, err := openSQLiteRuntimeHandle(dbPath, false)
	if err != nil {
		return err
	}
	if err := ensureFreshSQLiteSchema(context.Background(), db, dbPath); err != nil {
		_ = db.Close()
		return err
	}

	// The migration set is the PostgreSQL one: sqlitecompat translates column
	// types, constraints and defaults while applying it.
	if err := sqlitecompat.ApplyMigrations(context.Background(), db, sqliteMigrations()); err != nil {
		_ = db.Close()
		return fmt.Errorf("usage: apply sqlite migrations: %w", err)
	}

	readDB, err := openSQLiteRuntimeHandle(dbPath, true)
	if err != nil {
		_ = db.Close()
		return err
	}

	// runSQLiteBootstrap stays false: the migration set above already created
	// every table and column the SQLite bootstrap used to create ad hoc.
	if err := initOpenedDBLocked(db, readDB, dbPath, "sqlite", storageCfg, loc, false); err != nil {
		_ = db.Close()
		_ = readDB.Close()
		return err
	}
	log.Infof("usage: runtime database is sqlite at %s", dbPath)
	return nil
}

// ensureFreshSQLiteSchema refuses a database file that already holds tables but
// no migration history. Such a file predates this runtime (the pre-PostgreSQL
// builds created their tables ad hoc), and half-applying the migration set on
// top of it would corrupt live data. Pointing sqlite.path at a new file, or
// restoring a backup and migrating it deliberately, must be an explicit choice.
func ensureFreshSQLiteSchema(ctx context.Context, db *sql.DB, dbPath string) error {
	var tables int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`,
	).Scan(&tables); err != nil {
		return fmt.Errorf("usage: inspect sqlite schema: %w", err)
	}
	if tables == 0 {
		return nil
	}
	var migrations int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&migrations); err != nil {
		return fmt.Errorf("usage: inspect sqlite migration history: %w", err)
	}
	if migrations > 0 {
		return nil
	}
	return fmt.Errorf(
		"usage: sqlite database %s already contains %d table(s) but no migration history; "+
			"it was written by an older build. Back it up, then either point sqlite.path at a new file "+
			"or migrate the existing data into PostgreSQL",
		dbPath, tables)
}

func sqliteMigrations() []sqlitecompat.Migration {
	runtimeMigrations := postgres.RuntimeMigrations()
	migrations := make([]sqlitecompat.Migration, 0, len(runtimeMigrations))
	for _, migration := range runtimeMigrations {
		migrations = append(migrations, sqlitecompat.Migration{Version: migration.Version, SQL: migration.SQL})
	}
	return migrations
}

func openSQLiteRuntimeHandle(dbPath string, readOnly bool) (*sql.DB, error) {
	db, err := sql.Open(sqlitecompat.DriverName, sqliteRuntimeDSN(dbPath, readOnly))
	if err != nil {
		return nil, fmt.Errorf("usage: open sqlite: %w", err)
	}
	if readOnly {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(30 * time.Minute)
	} else {
		// WAL lets readers and the writer coexist; the pool still needs more
		// than one connection because every runtime service shares this handle
		// and SQLite serialises the writes itself through busy_timeout.
		db.SetMaxOpenConns(sqliteRuntimeMaxOpenConns)
		db.SetMaxIdleConns(sqliteRuntimeMaxOpenConns)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("usage: ping sqlite: %w", err)
	}
	return db, nil
}

// sqliteRuntimeMaxOpenConns is deliberately small: SQLite has one writer at a
// time, and busy_timeout serialises the rest.
const sqliteRuntimeMaxOpenConns = 8

// sqliteRuntimeDSN puts the connection pragmas in the DSN so every pooled
// connection gets them. modernc.org/sqlite runs each _pragma on connect.
func sqliteRuntimeDSN(dbPath string, readOnly bool) string {
	params := []string{
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(0)",
	}
	if readOnly {
		params = append(params, "mode=ro")
		return "file:" + dbPath + "?" + strings.Join(params, "&")
	}
	params = append(params,
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		// Begin write transactions as IMMEDIATE so a read-then-write upgrade
		// cannot fail with SQLITE_BUSY halfway through.
		"_txlock=immediate",
	)
	return "file:" + dbPath + "?" + strings.Join(params, "&")
}
