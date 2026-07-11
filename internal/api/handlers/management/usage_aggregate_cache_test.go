package management

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestUsageAggregateCacheKeyCanonicalizesFiltersWithoutRetainingSecrets(t *testing.T) {
	left := usageFilters{
		Page:        1,
		Size:        50,
		Days:        7,
		APIKey:      "sk-secret-value",
		Model:       "gpt-5",
		AuthIndexes: []string{" auth-b ", "auth-a", "auth-b"},
		Sources:     []string{"secret-source-b", "secret-source-a", "secret-source-b"},
	}
	right := left
	right.AuthIndexes = []string{"auth-a", "auth-b"}
	right.Sources = []string{"secret-source-a", "secret-source-b"}

	leftKey := usageAggregateCacheKey(usageAggregateChart, left)
	rightKey := usageAggregateCacheKey(usageAggregateChart, right)
	if leftKey != rightKey {
		t.Fatalf("canonical keys differ: %q != %q", leftKey, rightKey)
	}
	for _, secret := range []string{"sk-secret-value", "secret-source-a", "secret-source-b"} {
		if strings.Contains(leftKey, secret) {
			t.Fatalf("cache key retains plaintext secret %q: %q", secret, leftKey)
		}
	}
	if entityKey := usageAggregateCacheKey(usageAggregateEntity, left); entityKey == leftKey {
		t.Fatalf("different aggregate kinds must not share a cache key: %q", leftKey)
	}
}

func TestUsageAggregateCacheCoalescesConcurrentBuildsAndCachesResult(t *testing.T) {
	h := &Handler{usageAggregateCacheTTL: time.Minute}
	filters := usageFilters{Days: 7, AuthIndexes: []string{"auth-b", "auth-a"}}
	var builds atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	build := func(ctx context.Context) (gin.H, error) {
		builds.Add(1)
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
			return gin.H{"value": "shared"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	const callers = 8
	begin := make(chan struct{})
	results := make(chan gin.H, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-begin
			payload, err := h.loadUsageAggregate(context.Background(), usageAggregateChart, filters, build)
			results <- payload
			errs <- err
		}()
	}
	close(begin)
	<-started
	time.Sleep(25 * time.Millisecond)
	close(release)

	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if payload := <-results; payload["value"] != "shared" {
			t.Fatalf("caller %d payload = %#v", i, payload)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("build count = %d, want 1", got)
	}

	payload, err := h.loadUsageAggregate(context.Background(), usageAggregateChart, filters, func(context.Context) (gin.H, error) {
		return nil, errors.New("cache miss")
	})
	if err != nil || payload["value"] != "shared" {
		t.Fatalf("cached result = %#v, %v", payload, err)
	}
}

func TestUsageAggregateCacheLetsWaitersCancelWithoutCancelingSharedBuild(t *testing.T) {
	h := &Handler{usageAggregateCacheTTL: time.Minute}
	filters := usageFilters{Days: 7}
	started := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		_, err := h.loadUsageAggregate(context.Background(), usageAggregateChart, filters, func(context.Context) (gin.H, error) {
			close(started)
			<-release
			return gin.H{"value": "complete"}, nil
		})
		leaderDone <- err
	}()
	<-started

	waiterContext, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := h.loadUsageAggregate(waiterContext, usageAggregateChart, filters, func(context.Context) (gin.H, error) {
			return nil, errors.New("waiter unexpectedly became leader")
		})
		waiterDone <- err
	}()
	cancelWaiter()
	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error = %v, want context canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("canceled waiter remained blocked on the shared build")
	}

	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader error = %v", err)
	}
}

func TestUsageAggregateCacheDoesNotCacheCanceledOrFailedBuilds(t *testing.T) {
	h := &Handler{usageAggregateCacheTTL: time.Minute}
	filters := usageFilters{Days: 7}

	leaderContext, cancelLeader := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := h.loadUsageAggregate(leaderContext, usageAggregateEntity, filters, func(ctx context.Context) (gin.H, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		done <- err
	}()
	<-started
	cancelLeader()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build error = %v", err)
	}

	failedFilters := usageFilters{Days: 30}
	failedBuilds := 0
	_, err := h.loadUsageAggregate(context.Background(), usageAggregateEntity, failedFilters, func(context.Context) (gin.H, error) {
		failedBuilds++
		return nil, errors.New("query failed")
	})
	if err == nil {
		t.Fatal("failed build unexpectedly succeeded")
	}
	payload, err := h.loadUsageAggregate(context.Background(), usageAggregateEntity, failedFilters, func(context.Context) (gin.H, error) {
		failedBuilds++
		return gin.H{"value": "retry"}, nil
	})
	if err != nil || payload["value"] != "retry" {
		t.Fatalf("retry result = %#v, %v", payload, err)
	}
	if failedBuilds != 2 {
		t.Fatalf("post-cancellation build count = %d, want 2", failedBuilds)
	}
}

func TestUsageAggregateCacheRetriesLiveWaiterAfterLeaderCancellation(t *testing.T) {
	h := &Handler{usageAggregateCacheTTL: time.Minute}
	filters := usageFilters{Days: 7, Sources: []string{"codex"}}

	leaderContext, cancelLeader := context.WithCancel(context.Background())
	leaderStarted := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		_, err := h.loadUsageAggregate(leaderContext, usageAggregateEntity, filters, func(ctx context.Context) (gin.H, error) {
			close(leaderStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		leaderDone <- err
	}()
	<-leaderStarted

	cancelScheduled := make(chan struct{})
	go func() {
		close(cancelScheduled)
		time.Sleep(100 * time.Millisecond)
		cancelLeader()
	}()
	<-cancelScheduled

	var waiterBuilds atomic.Int32
	payload, err := h.loadUsageAggregate(context.Background(), usageAggregateEntity, filters, func(context.Context) (gin.H, error) {
		waiterBuilds.Add(1)
		return gin.H{"value": "waiter-retry"}, nil
	})
	if err != nil || payload["value"] != "waiter-retry" {
		t.Fatalf("live waiter result = %#v, %v", payload, err)
	}
	if got := waiterBuilds.Load(); got != 1 {
		t.Fatalf("waiter build count = %d, want 1", got)
	}
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context canceled", err)
	}

	cached, err := h.loadUsageAggregate(context.Background(), usageAggregateEntity, filters, func(context.Context) (gin.H, error) {
		return nil, errors.New("successful waiter retry was not cached")
	})
	if err != nil || cached["value"] != "waiter-retry" {
		t.Fatalf("cached waiter retry = %#v, %v", cached, err)
	}
}

func TestUsageAggregateCacheExpiresBoundsEntriesAndInvalidatesOnConfigChange(t *testing.T) {
	now := time.Unix(100, 0)
	h := &Handler{
		usageAggregateCacheTTL: time.Second,
		usageAggregateCacheNow: func() time.Time { return now },
	}
	builds := 0
	load := func(filters usageFilters) {
		t.Helper()
		_, err := h.loadUsageAggregate(context.Background(), usageAggregateChart, filters, func(context.Context) (gin.H, error) {
			builds++
			return gin.H{"build": builds}, nil
		})
		if err != nil {
			t.Fatalf("load aggregate: %v", err)
		}
	}

	base := usageFilters{Days: 7, APIKey: "key-0"}
	load(base)
	load(base)
	if builds != 1 {
		t.Fatalf("unexpired build count = %d, want 1", builds)
	}
	now = now.Add(2 * time.Second)
	load(base)
	if builds != 2 {
		t.Fatalf("expired build count = %d, want 2", builds)
	}

	for i := 1; i <= usageAggregateCacheMaxEntries+4; i++ {
		load(usageFilters{Days: 7, APIKey: "key-" + time.Unix(int64(i), 0).Format(time.RFC3339Nano)})
	}
	h.usageAggregateCacheMu.Lock()
	cacheLen := len(h.usageAggregateCache)
	h.usageAggregateCacheMu.Unlock()
	if cacheLen > usageAggregateCacheMaxEntries {
		t.Fatalf("cache entries = %d, want at most %d", cacheLen, usageAggregateCacheMaxEntries)
	}

	load(base)
	beforeInvalidation := builds
	h.SetConfig(&config.Config{})
	load(base)
	if builds != beforeInvalidation+1 {
		t.Fatalf("config invalidation did not rebuild: before=%d after=%d", beforeInvalidation, builds)
	}
}

func TestUsageAggregateHandlersStopSQLiteQueriesWhenRequestIsCanceled(t *testing.T) {
	for name, handler := range map[string]func(*Handler, *gin.Context){
		"chart":  func(h *Handler, c *gin.Context) { h.GetUsageChartData(c) },
		"entity": func(h *Handler, c *gin.Context) { h.GetUsageEntityStats(c) },
	} {
		t.Run(name, func(t *testing.T) {
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
			ginContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/"+name+"?days=7", nil).WithContext(requestContext)

			done := make(chan struct{})
			go func() {
				handler(h, ginContext)
				close(done)
			}()

			deadline := time.NewTimer(2 * time.Second)
			ticker := time.NewTicker(time.Millisecond)
			defer deadline.Stop()
			defer ticker.Stop()
			for dashboardCancelProbeCalls.Load() == 0 {
				select {
				case <-ticker.C:
				case <-deadline.C:
					t.Fatal("usage aggregate did not reach the SQLite cancellation probe")
				}
			}

			cancel()
			select {
			case <-done:
			case <-time.After(300 * time.Millisecond):
				t.Fatalf("%s query kept running after request cancellation", name)
			}
		})
	}
}

func TestDeleteUsageRecordsInvalidatesAggregateCache(t *testing.T) {
	h := newUsageContractTestHandler(t)
	status, before := performUsageContractRequest(t, http.MethodGet, "/v0/management/usage/chart-data?days=7", nil, nil, h.GetUsageChartData)
	if status != http.StatusOK || before["stats"].(map[string]any)["total"].(float64) != 2 {
		t.Fatalf("unexpected chart before delete: status=%d payload=%#v", status, before)
	}

	deleteBody := []byte(`{"clear_body_content":false,"clear_detail_content":false,"clear_request_records":true}`)
	status, deleted := performUsageContractRequest(t, http.MethodDelete, "/v0/management/usage/logs", deleteBody, nil, h.DeleteUsageLogs)
	if status != http.StatusOK || deleted["deleted_logs"].(float64) != 3 {
		t.Fatalf("unexpected delete response: status=%d payload=%#v", status, deleted)
	}

	status, after := performUsageContractRequest(t, http.MethodGet, "/v0/management/usage/chart-data?days=7", nil, nil, h.GetUsageChartData)
	if status != http.StatusOK || after["stats"].(map[string]any)["total"].(float64) != 0 {
		t.Fatalf("stale chart after delete: status=%d payload=%#v", status, after)
	}
}
