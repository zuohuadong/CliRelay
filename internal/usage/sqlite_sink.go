package usage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

var (
	defaultSink               = &sqliteSink{}
	registerSinkOnce          sync.Once
	billingMultiplierSnapshot = channelBillingMultipliers{byChannel: map[string]float64{}}
	billingMultiplierMu       sync.RWMutex
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

type channelBillingMultipliers struct {
	byChannel map[string]float64
}

// SetChannelBillingMultipliersFromConfig refreshes the runtime channel pricing snapshot.
func SetChannelBillingMultipliersFromConfig(cfg *config.Config) {
	next := channelBillingMultipliers{byChannel: map[string]float64{}}
	if cfg != nil {
		for channel, multiplier := range cfg.BillingMultipliers {
			setBillingMultiplier(next.byChannel, channel, multiplier)
		}
		for _, entry := range cfg.GeminiKey {
			setBillingMultiplier(next.byChannel, simpleChannelName(entry.Prefix, "gemini"), entry.BillingMultiplier)
		}
		for _, entry := range cfg.ClaudeKey {
			setBillingMultiplier(next.byChannel, simpleChannelName(entry.Prefix, "claude"), entry.BillingMultiplier)
		}
		for _, entry := range cfg.CodexKey {
			setBillingMultiplier(next.byChannel, simpleChannelName(entry.Prefix, "codex"), entry.BillingMultiplier)
		}
		for _, entry := range cfg.OpenCodeGoKey {
			setBillingMultiplier(next.byChannel, entry.Name, entry.BillingMultiplier)
		}
		for _, entry := range cfg.BedrockKey {
			setBillingMultiplier(next.byChannel, entry.Name, entry.BillingMultiplier)
		}
		for _, entry := range cfg.VertexCompatAPIKey {
			setBillingMultiplier(next.byChannel, simpleChannelName(entry.Prefix, "vertex"), entry.BillingMultiplier)
		}
		for _, entry := range cfg.OpenAICompatibility {
			setBillingMultiplier(next.byChannel, entry.Name, entry.BillingMultiplier)
		}
		for _, entry := range cfg.BigModelCodingAPIKey {
			setBillingMultiplier(next.byChannel, simpleChannelName(entry.Name, config.DefaultBigModelCodingProviderName), entry.BillingMultiplier)
		}
		for _, entry := range cfg.AstronCodeAPIKey {
			setBillingMultiplier(next.byChannel, simpleChannelName(entry.Name, config.DefaultAstronCodeProviderName), entry.BillingMultiplier)
		}
		for _, entry := range cfg.AgnesAPIKey {
			setBillingMultiplier(next.byChannel, simpleChannelName(entry.Name, config.DefaultAgnesProviderName), entry.BillingMultiplier)
		}
	}
	billingMultiplierMu.Lock()
	billingMultiplierSnapshot = next
	billingMultiplierMu.Unlock()
}

func setBillingMultiplier(target map[string]float64, channel string, multiplier float64) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" || multiplier <= 0 {
		return
	}
	target[channel] = multiplier
}

func simpleChannelName(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

func billingMultiplierForRecord(record coreusage.Record) float64 {
	candidates := []string{
		record.Provider,
		record.Source,
		record.AuthType,
	}
	billingMultiplierMu.RLock()
	defer billingMultiplierMu.RUnlock()
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		if key == "" {
			continue
		}
		if multiplier := billingMultiplierSnapshot.byChannel[key]; multiplier > 0 {
			return multiplier
		}
	}
	return 1
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

	// Cost is computed from the actual upstream model that handled the request
	// (record.Model), never from the client alias. Failed requests cost nothing.
	modelName := nonEmpty(record.Model, "unknown")
	requestCost := 0.0
	if failed == 0 {
		requestCost = CalculateModelCostWithDB(db, modelName,
			detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens, detail.CachedTokens)
		requestCost *= billingMultiplierForRecord(record)
	}
	apiKeyName := LookupAPIKeyNameWithDB(db, record.APIKey)

	insertCtx := context.WithoutCancel(ctx)
	_, errInsert := db.ExecContext(insertCtx, `
INSERT INTO request_logs (
  timestamp, api_key, model, source, channel_name, auth_index, failed,
  latency_ms, first_token_ms, input_tokens, output_tokens, reasoning_tokens,
  cached_tokens, total_tokens, input_content, output_content, cost, api_key_name
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, '', '', ?, ?)
`, timestamp.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(record.APIKey),
		modelName,
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
		requestCost,
		apiKeyName,
	)
	if errInsert != nil {
		if errors.Is(errInsert, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			log.WithError(errInsert).Debug("usage sqlite sink: insert request log canceled")
			return
		}
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
CREATE INDEX IF NOT EXISTS idx_logs_api_key_timestamp ON request_logs(api_key, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_logs_model ON request_logs(model);
CREATE INDEX IF NOT EXISTS idx_logs_failed ON request_logs(failed);
CREATE INDEX IF NOT EXISTS idx_logs_auth_index ON request_logs(auth_index);
`)
	if err != nil {
		return err
	}
	// Ensure the pricing table exists and is seeded with official platform
	// prices so per-request cost can be computed from the first insert onward.
	EnsureModelPricesTable(db)
	SeedOfficialModelPrices(db)
	return nil
}

func nonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func getDB() *sql.DB {
	defaultSink.mu.Lock()
	defer defaultSink.mu.Unlock()
	db, _ := defaultSink.databaseLocked()
	return db
}

// ChannelLatency holds the average latency stats for a single channel (source).
type ChannelLatency struct {
	Source string  `json:"source"`
	Count  int64   `json:"count"`
	AvgMs  float64 `json:"avg_ms"`
}

// GetChannelAvgLatency returns average request latency grouped by source (channel)
// for the last N days.
func GetChannelAvgLatency(days int) ([]ChannelLatency, error) {
	db := getDB()
	if db == nil {
		return nil, nil
	}
	if days < 1 {
		days = 7
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339)
	rows, err := db.Query(`
		SELECT source, COUNT(*) as cnt, AVG(latency_ms) as avg_lat
		FROM request_logs
		WHERE timestamp > ? AND source != ''
		GROUP BY source
		ORDER BY avg_lat DESC
		LIMIT 10
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []ChannelLatency
	for rows.Next() {
		var cl ChannelLatency
		if err := rows.Scan(&cl.Source, &cl.Count, &cl.AvgMs); err == nil {
			result = append(result, cl)
		}
	}
	return result, rows.Err()
}
