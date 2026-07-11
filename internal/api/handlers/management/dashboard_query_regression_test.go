package management

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"modernc.org/sqlite"
)

var (
	dashboardCancelProbeDelayNS atomic.Int64
	dashboardCancelProbeCalls   atomic.Int64
)

func init() {
	sqlite.MustRegisterCollationUtf8(
		"dashboard_cancel_probe_v1",
		func(left, right string) int {
			dashboardCancelProbeCalls.Add(1)
			if delay := time.Duration(dashboardCancelProbeDelayNS.Load()); delay > 0 {
				time.Sleep(delay)
			}
			return strings.Compare(left, right)
		},
	)
}

func TestUsageTimeRangeUsesTimestampIndex(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err = db.Exec(`
		CREATE TABLE request_logs (
			id INTEGER PRIMARY KEY,
			timestamp DATETIME NOT NULL
		);
		CREATE INDEX idx_logs_timestamp ON request_logs(timestamp DESC);
	`); err != nil {
		t.Fatalf("create request log schema: %v", err)
	}

	whereSQL, args := (usageFilters{Days: 7}).whereClause(db)
	rows, err := db.Query("EXPLAIN QUERY PLAN SELECT count(*) FROM request_logs WHERE "+whereSQL, args...)
	if err != nil {
		t.Fatalf("explain usage range query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if errScan := rows.Scan(&id, &parent, &unused, &detail); errScan != nil {
			t.Fatalf("scan query plan: %v", errScan)
		}
		details = append(details, detail)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "SEARCH request_logs") || !strings.Contains(plan, "idx_logs_timestamp") {
		t.Fatalf("usage time range must SEARCH idx_logs_timestamp, plan:\n%s", plan)
	}
	if strings.Contains(plan, "SCAN request_logs") {
		t.Fatalf("usage time range must not scan request_logs, plan:\n%s", plan)
	}
}

func TestUsageStartEndRangePreservesRFC3339SecondSemantics(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err = db.Exec(`
		CREATE TABLE request_logs (
			id INTEGER PRIMARY KEY,
			timestamp DATETIME NOT NULL
		);
		CREATE INDEX idx_logs_timestamp ON request_logs(timestamp DESC);
		INSERT INTO request_logs (id, timestamp) VALUES
			(1, '2026-06-30T23:59:59.999999999Z'),
			(2, '2026-07-01T00:00:00Z'),
			(3, '2026-07-01T00:00:00.999999999Z'),
			(4, '2026-07-01T00:00:01Z');
	`); err != nil {
		t.Fatalf("create request log fixture: %v", err)
	}

	filters := usageFilters{
		Start: "2026-07-01T02:00:00+02:00",
		End:   "2026-07-01T00:00:00.100000000Z",
	}
	whereSQL, args := filters.whereClause(db)
	var count int
	if err = db.QueryRow("SELECT count(*) FROM request_logs WHERE "+whereSQL, args...).Scan(&count); err != nil {
		t.Fatalf("query RFC3339 range: %v", err)
	}
	if count != 2 {
		t.Fatalf("RFC3339 start/end range count = %d, want 2 records from the inclusive boundary second", count)
	}
}

func TestDashboardSummaryStopsSQLiteQueryWhenRequestContextIsCanceled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "usage.db"))
	if err != nil {
		t.Fatalf("open usage db: %v", err)
	}
	if _, err = db.Exec(`
		CREATE TABLE request_logs (
			id INTEGER PRIMARY KEY,
			timestamp TEXT COLLATE dashboard_cancel_probe_v1 NOT NULL,
			failed INTEGER NOT NULL DEFAULT 0
		);
		WITH RECURSIVE seq(id) AS (
			SELECT 1
			UNION ALL
			SELECT id + 1 FROM seq WHERE id < 200
		)
		INSERT INTO request_logs (id, timestamp)
		SELECT id, strftime('%Y-%m-%dT%H:%M:%fZ', 'now') FROM seq;
	`); err != nil {
		_ = db.Close()
		t.Fatalf("create slow usage fixture: %v", err)
	}
	if err = db.Close(); err != nil {
		t.Fatalf("close usage fixture: %v", err)
	}

	dashboardCancelProbeCalls.Store(0)
	dashboardCancelProbeDelayNS.Store(int64(5 * time.Millisecond))
	t.Cleanup(func() { dashboardCancelProbeDelayNS.Store(0) })

	h := &Handler{cfg: &config.Config{}, configFilePath: filepath.Join(dir, "config.yaml")}
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	requestContext, cancel := context.WithCancel(context.Background())
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/dashboard-summary?days=7", nil).WithContext(requestContext)

	done := make(chan struct{})
	go func() {
		h.GetDashboardSummary(ginContext)
		close(done)
	}()

	probeDeadline := time.NewTimer(2 * time.Second)
	probeTicker := time.NewTicker(time.Millisecond)
	defer probeDeadline.Stop()
	defer probeTicker.Stop()
	for dashboardCancelProbeCalls.Load() == 0 {
		select {
		case <-probeTicker.C:
		case <-probeDeadline.C:
			t.Fatal("dashboard query did not reach the SQLite cancellation probe")
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("dashboard query kept running after request cancellation; probe calls=%d", dashboardCancelProbeCalls.Load())
	}
}
