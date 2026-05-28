package usage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	_ "modernc.org/sqlite"
)

func TestSQLiteSinkWritesRequestLog(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data", "usage.db")
	sink := &sqliteSink{}
	sink.SetPath(dbPath)

	prevUsageEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetUsageStatisticsEnabled(prevUsageEnabled)
	})

	sink.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "bigmodel-coding",
		Model:       "glm-5.1",
		APIKey:      "sk-test",
		AuthIndex:   "auth-1",
		Source:      "user@example.com",
		RequestedAt: time.Date(2026, 5, 26, 1, 2, 3, 4, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     10,
			OutputTokens:    20,
			ReasoningTokens: 3,
			CachedTokens:    4,
		},
	})

	db, errOpen := sql.Open("sqlite", dbPath)
	if errOpen != nil {
		t.Fatalf("open usage db: %v", errOpen)
	}
	defer func() {
		if errClose := db.Close(); errClose != nil {
			t.Fatalf("close usage db: %v", errClose)
		}
	}()

	var got struct {
		Timestamp       string
		APIKey          string
		Model           string
		Source          string
		ChannelName     string
		AuthIndex       string
		Failed          int
		LatencyMs       int64
		InputTokens     int64
		OutputTokens    int64
		ReasoningTokens int64
		CachedTokens    int64
		TotalTokens     int64
	}
	errScan := db.QueryRow(`
SELECT timestamp, api_key, model, source, channel_name, auth_index, failed,
       latency_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens
FROM request_logs
`).Scan(
		&got.Timestamp,
		&got.APIKey,
		&got.Model,
		&got.Source,
		&got.ChannelName,
		&got.AuthIndex,
		&got.Failed,
		&got.LatencyMs,
		&got.InputTokens,
		&got.OutputTokens,
		&got.ReasoningTokens,
		&got.CachedTokens,
		&got.TotalTokens,
	)
	if errScan != nil {
		t.Fatalf("scan request log: %v", errScan)
	}

	if got.Timestamp != "2026-05-26T01:02:03.000000004Z" {
		t.Fatalf("timestamp = %q", got.Timestamp)
	}
	if got.APIKey != "sk-test" || got.Model != "glm-5.1" || got.Source != "user@example.com" {
		t.Fatalf("unexpected identity fields: %+v", got)
	}
	if got.ChannelName != "bigmodel-coding" || got.AuthIndex != "auth-1" {
		t.Fatalf("unexpected route fields: %+v", got)
	}
	if got.Failed != 0 || got.LatencyMs != 1500 {
		t.Fatalf("unexpected outcome fields: %+v", got)
	}
	if got.InputTokens != 10 || got.OutputTokens != 20 || got.ReasoningTokens != 3 || got.CachedTokens != 4 || got.TotalTokens != 33 {
		t.Fatalf("unexpected token fields: %+v", got)
	}
}

func TestEnsureAPIKeysTableMigratesLegacySchema(t *testing.T) {
	db, errOpen := sql.Open("sqlite", filepath.Join(t.TempDir(), "usage.db"))
	if errOpen != nil {
		t.Fatalf("open db: %v", errOpen)
	}
	defer func() {
		if errClose := db.Close(); errClose != nil {
			t.Fatalf("close db: %v", errClose)
		}
	}()

	if _, errExec := db.Exec(`create table api_keys (key text not null primary key, name text not null default '')`); errExec != nil {
		t.Fatalf("create legacy api_keys table: %v", errExec)
	}

	EnsureAPIKeysTable(db)

	for _, column := range []string{"daily_limit", "allowed_channel_groups", "system_prompt", "permission_profile_id"} {
		if !apiKeysDBHasColumn(db, column) {
			t.Fatalf("expected migrated column %s", column)
		}
	}
}

func TestSQLiteSinkIgnoresCanceledContextNoise(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data", "usage.db")
	sink := &sqliteSink{}
	sink.SetPath(dbPath)

	prevUsageEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetUsageStatisticsEnabled(prevUsageEnabled)
	})

	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink.HandleUsage(ctx, coreusage.Record{
		Provider: "codex",
		Model:    "gpt-5.5",
	})

	for _, entry := range hook.AllEntries() {
		if entry.Level <= log.WarnLevel && strings.Contains(entry.Message, "usage sqlite sink: insert request log") {
			t.Fatalf("unexpected warning for canceled context: %s", entry.Message)
		}
	}
}
