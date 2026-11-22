package usage

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/testutil/postgrestest"
)

func TestPostgresRuntimeLocksAndQueue(t *testing.T) {
	_, dsn := postgrestest.RuntimeState(t)
	if err := InitPostgres(config.PostgresConfig{DSN: dsn}, config.RequestLogStorageConfig{}, time.UTC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			ok, err := AcquireRuntimeLock(ctx, "rebuild", strconv.Itoa(i), time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("got %d lock holders, want one", winners.Load())
	}
	if ok, err := ReleaseRuntimeLock(ctx, "rebuild", "not-owner"); err != nil || ok {
		t.Fatalf("non-owner release: %v %v", ok, err)
	}
	if _, err := RuntimeDB().ExecContext(ctx, `UPDATE runtime_named_locks SET expires_at = clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := AcquireRuntimeLock(ctx, "rebuild", "next", time.Minute); err != nil || !ok {
		t.Fatalf("take expired lock: %v %v", ok, err)
	}
	if ok, err := ReleaseRuntimeLock(ctx, "rebuild", "next"); err != nil || !ok {
		t.Fatalf("release new lock: %v %v", ok, err)
	}

	for i := range 4 {
		err := EnqueueRuntimeJob(ctx, "jobs", RuntimeJob{ID: strconv.Itoa(i), Kind: "refresh"})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2 {
		job, err := DequeueRuntimeJob(ctx, "jobs", time.Second)
		if err != nil || job == nil || job.ID != strconv.Itoa(i) {
			t.Fatalf("FIFO pop %d: %+v %v", i, job, err)
		}
	}
	results := make(chan string, 2)
	for range 2 {
		wg.Go(func() {
			job, err := DequeueRuntimeJob(ctx, "jobs", time.Second)
			if err != nil || job == nil {
				t.Errorf("parallel pop: %+v %v", job, err)
				return
			}
			results <- job.ID
		})
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for result := range results {
		seen[result] = true
	}
	if !seen["2"] || !seen["3"] {
		t.Fatalf("parallel workers did not claim distinct jobs: %v", seen)
	}
	if job, err := DequeueRuntimeJob(ctx, "jobs", 20*time.Millisecond); err != nil || job != nil {
		t.Fatalf("empty queue timeout: %+v %v", job, err)
	}
	block, stop := context.WithCancel(ctx)
	stop()
	if _, err := DequeueRuntimeJob(block, "jobs", 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled blocking pop: %v", err)
	}
}
