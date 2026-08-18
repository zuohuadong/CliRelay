package cliproxy

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	_ "modernc.org/sqlite"
)

func TestServiceRunRegistersSQLiteUsageSink(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 0\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	cfg := &config.Config{
		Port:                   0,
		AuthDir:                tempDir,
		UsageStatisticsEnabled: true,
	}

	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}

	prevUsageEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetUsageStatisticsEnabled(prevUsageEnabled)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = service.Run(ctx)
	}()

	// Wait briefly for service Run to initialize lifecycle
	time.Sleep(100 * time.Millisecond)

	usage.PublishRecord(context.Background(), usage.Record{
		Provider:    "openai",
		Model:       "gpt-5.4",
		APIKey:      "sk-lifecycle-test",
		RequestedAt: time.Now().UTC(),
		Latency:     250 * time.Millisecond,
		TTFT:        50 * time.Millisecond,
		Detail: usage.Detail{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	})

	// Cancel and wait for service shutdown
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for service shutdown")
	}

	usageDBPath := filepath.Join(tempDir, "data", "usage.db")
	db, errOpen := sql.Open("sqlite", usageDBPath)
	if errOpen != nil {
		t.Fatalf("open usage db: %v", errOpen)
	}
	defer func() { _ = db.Close() }()

	var model, apiKey string
	var totalTokens int64
	errScan := db.QueryRow(`SELECT model, api_key, total_tokens FROM request_logs ORDER BY id DESC LIMIT 1`).Scan(&model, &apiKey, &totalTokens)
	if errScan != nil {
		t.Fatalf("scan request_logs: %v", errScan)
	}

	if model != "gpt-5.4" || apiKey != "sk-lifecycle-test" || totalTokens != 150 {
		t.Fatalf("unexpected record: model=%s, apiKey=%s, totalTokens=%d", model, apiKey, totalTokens)
	}
}
