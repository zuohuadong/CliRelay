package config

import (
	"path/filepath"
	"strings"
)

// EnvSQLitePath overrides sqlite.path.
const EnvSQLitePath = "CLIRELAY_SQLITE_PATH"

// UsesSQLiteRuntime reports whether the runtime should use the embedded
// database instead of PostgreSQL. An empty postgres.dsn means single node.
func (cfg *Config) UsesSQLiteRuntime() bool {
	if cfg == nil {
		return false
	}
	return strings.TrimSpace(cfg.Postgres.DSN) == ""
}

// SQLiteRuntimeDBPath resolves the runtime database file. An empty sqlite.path
// keeps the historical layout next to config.yaml: <config dir>/data/usage.db,
// which is where the pre-PostgreSQL builds kept it, so an upgrade reuses the
// existing file instead of starting from an empty database.
func (cfg *Config) SQLiteRuntimeDBPath(configPath string) string {
	if cfg != nil {
		if path := strings.TrimSpace(cfg.SQLite.Path); path != "" {
			return path
		}
	}
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return filepath.Join("data", "usage.db")
	}
	return filepath.Join(filepath.Dir(configPath), "data", "usage.db")
}
