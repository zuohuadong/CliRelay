package usage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

var (
	defaultSink      = &sqliteSink{}
	registerSinkOnce sync.Once
)

// RegisterSQLiteSink stores runtime usage records in the management usage database.
func RegisterSQLiteSink(dbPath string) {
	defaultSink.SetPath(dbPath)
	registerSinkOnce.Do(func() {
		coreusage.RegisterPlugin(defaultSink)
	})
}

// SQLiteDBPathForConfig returns the management usage database path for a config file.
func SQLiteDBPathForConfig(configPath string) string {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), "data", "usage.db")
}

type sqliteSink struct {
	mu   sync.Mutex
	path string
	db   *sql.DB
}

func (s *sqliteSink) SetPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path = strings.TrimSpace(path)
	if path == s.path {
		return
	}
	if s.db != nil {
		if errClose := s.db.Close(); errClose != nil {
			log.WithError(errClose).Warn("usage sqlite sink: close previous database")
		}
		s.db = nil
	}
	s.path = path
}

func (s *sqliteSink) HandleUsage(ctx context.Context, record coreusage.Record) {
	if s == nil || !redisqueue.UsageStatisticsEnabled() {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	db, ok := s.databaseLocked()
	if !ok {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	failed := 0
	if record.Failed || internallogging.GetResponseStatus(ctx) >= 400 {
		failed = 1
	}

	detail := record.Detail
	totalTokens := detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	if totalTokens == 0 {
		totalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens + detail.CachedTokens
	}

	_, errInsert := db.ExecContext(ctx, `
INSERT INTO request_logs (
  timestamp, api_key, model, source, channel_name, auth_index, failed,
  latency_ms, first_token_ms, input_tokens, output_tokens, reasoning_tokens,
  cached_tokens, total_tokens, input_content, output_content, cost, api_key_name
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, '', '', 0, '')
`, timestamp.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(record.APIKey),
		nonEmpty(record.Model, "unknown"),
		strings.TrimSpace(record.Source),
		strings.TrimSpace(record.Provider),
		strings.TrimSpace(record.AuthIndex),
		failed,
		record.Latency.Milliseconds(),
		detail.InputTokens,
		detail.OutputTokens,
		detail.ReasoningTokens,
		detail.CachedTokens,
		totalTokens,
	)
	if errInsert != nil {
		log.WithError(errInsert).Warn("usage sqlite sink: insert request log")
	}
}

func (s *sqliteSink) databaseLocked() (*sql.DB, bool) {
	if strings.TrimSpace(s.path) == "" {
		return nil, false
	}
	if s.db != nil {
		return s.db, true
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.path), 0o755); errMkdir != nil {
		log.WithError(errMkdir).Warn("usage sqlite sink: create data directory")
		return nil, false
	}
	db, errOpen := sql.Open("sqlite", s.path)
	if errOpen != nil {
		log.WithError(errOpen).Warn("usage sqlite sink: open database")
		return nil, false
	}
	db.SetMaxOpenConns(1)
	if errEnsure := ensureSchema(db); errEnsure != nil {
		if errClose := db.Close(); errClose != nil {
			log.WithError(errClose).Warn("usage sqlite sink: close database after schema failure")
		}
		log.WithError(errEnsure).Warn("usage sqlite sink: ensure schema")
		return nil, false
	}
	s.db = db
	return s.db, true
}

func ensureSchema(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		return err
	}
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS request_logs (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  timestamp        DATETIME NOT NULL,
  api_key          TEXT NOT NULL DEFAULT '',
  model            TEXT NOT NULL DEFAULT '',
  source           TEXT NOT NULL DEFAULT '',
  channel_name     TEXT NOT NULL DEFAULT '',
  auth_index       TEXT NOT NULL DEFAULT '',
  failed           INTEGER NOT NULL DEFAULT 0,
  latency_ms       INTEGER NOT NULL DEFAULT 0,
  first_token_ms   INTEGER NOT NULL DEFAULT 0,
  input_tokens     INTEGER NOT NULL DEFAULT 0,
  output_tokens    INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens    INTEGER NOT NULL DEFAULT 0,
  total_tokens     INTEGER NOT NULL DEFAULT 0,
  input_content    TEXT NOT NULL DEFAULT '',
  output_content   TEXT NOT NULL DEFAULT '',
  cost             REAL NOT NULL DEFAULT 0,
  api_key_name     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_logs_timestamp ON request_logs(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_logs_api_key ON request_logs(api_key);
CREATE INDEX IF NOT EXISTS idx_logs_model ON request_logs(model);
CREATE INDEX IF NOT EXISTS idx_logs_failed ON request_logs(failed);
CREATE INDEX IF NOT EXISTS idx_logs_auth_index ON request_logs(auth_index);
`)
	return err
}

func nonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
