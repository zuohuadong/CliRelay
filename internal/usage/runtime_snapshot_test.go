package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/testutil/postgrestest"
)

func TestPostgresSnapshotRestoresAndCombinesOverlappingNodes(t *testing.T) {
	db, _ := postgrestest.RuntimeState(t)
	ctx := context.Background()
	base := StatisticsSnapshot{
		TotalRequests: 5, SuccessCount: 3, FailureCount: 2, TotalTokens: 33,
		APIs: map[string]APISnapshot{"00000000-0000-4000-8000-000000000001": {
			TotalRequests: 5, TotalTokens: 33,
			Models: map[string]ModelSnapshot{"test-model": {TotalRequests: 5, TotalTokens: 33}},
		}},
		RequestsByDay: map[string]int64{"2026-11-22": 5}, RequestsByHour: map[string]int64{"05": 5},
		TokensByDay: map[string]int64{"2026-11-22": 33}, TokensByHour: map[string]int64{"05": 33},
	}
	data, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	a := &snapshotWriter{db: db, key: "cluster:one", stats: NewRequestStatistics()}
	b := &snapshotWriter{db: db, key: a.key, stats: NewRequestStatistics()}
	if err := a.restore(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := b.restore(ctx, path+"-not-needed"); err != nil {
		t.Fatal(err)
	}
	for _, w := range []*snapshotWriter{a, b} {
		snapshot := w.stats.Snapshot()
		if !reflect.DeepEqual(snapshot, base) {
			t.Fatalf("restored wrong statistics: %+v", snapshot)
		}
		accumulateSnapshot(&snapshot, base, 1)
		w.stats.restoreSnapshot(snapshot)
		if err := w.save(ctx); err != nil {
			t.Fatal(err)
		}
		if err := w.save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	want := StatisticsSnapshot{}
	accumulateSnapshot(&want, base, 3)
	if err := a.restore(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := a.stats.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted stats lost or duplicated concurrent updates: %+v", got)
	}
}
