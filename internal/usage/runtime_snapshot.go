package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cluster"
	log "github.com/sirupsen/logrus"
)

var snapshotLifecycle struct {
	sync.Mutex
	stop func()
}

type snapshotWriter struct {
	db    *sql.DB
	key   string
	stats *RequestStatistics
	saved StatisticsSnapshot
}

// StartRuntimeSnapshots restores exact aggregate counters and starts the
// PostgreSQL checkpoint loop. A legacy JSON export is imported only once.
func StartRuntimeSnapshots() error {
	db := RuntimeDB()
	if db == nil || RuntimeDriver() != "postgres" {
		return errors.New("usage checkpoint requires PostgreSQL")
	}
	snapshotLifecycle.Lock()
	defer snapshotLifecycle.Unlock()
	if snapshotLifecycle.stop != nil {
		return nil
	}
	key := "standalone"
	if c := cluster.Default(); c.Enabled() {
		key = "cluster:" + c.NodeID()
	}
	w := &snapshotWriter{db: db, key: key, stats: defaultRequestStatistics}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.restore(ctx, strings.TrimSpace(os.Getenv("CLIRELAY_USAGE_SNAPSHOT_IMPORT"))); err != nil {
		return err
	}
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				w.flush(runCtx)
			}
		}
	}()
	snapshotLifecycle.stop = func() {
		stop()
		<-done
		w.flush(context.Background())
	}
	return nil
}

func stopRuntimeSnapshots() {
	snapshotLifecycle.Lock()
	defer snapshotLifecycle.Unlock()
	if stop := snapshotLifecycle.stop; stop != nil {
		stop()
		snapshotLifecycle.stop = nil
	}
}

func (w *snapshotWriter) ensureRow(ctx context.Context, data []byte) error {
	_, err := w.db.ExecContext(ctx, `INSERT INTO runtime_usage_snapshots (node_id, payload)
		VALUES ($1, $2) ON CONFLICT (node_id) DO NOTHING`, w.key, data)
	return err
}

func (w *snapshotWriter) restore(ctx context.Context, legacyFile string) error {
	var data []byte
	err := w.db.QueryRowContext(ctx, `SELECT payload FROM runtime_usage_snapshots WHERE node_id = $1`, w.key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		data = []byte(`{}`)
		if legacyFile != "" {
			data, err = readLegacySnapshot(legacyFile)
			if err != nil {
				return err
			}
		}
		if err = w.ensureRow(ctx, data); err == nil {
			err = w.db.QueryRowContext(ctx, `SELECT payload FROM runtime_usage_snapshots WHERE node_id = $1`, w.key).Scan(&data)
		}
	}
	if err != nil {
		return err
	}
	var snapshot StatisticsSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	w.stats.restoreSnapshot(snapshot)
	w.saved = w.stats.Snapshot()
	return nil
}

func readLegacySnapshot(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open legacy usage snapshot")
	}
	defer f.Close()
	info, err := f.Stat()
	const maxSize = 64 << 20
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxSize {
		return nil, errors.New("legacy usage snapshot must be a private regular file of at most 64 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	var snapshot *StatisticsSnapshot
	if err != nil || len(data) > maxSize || json.Unmarshal(data, &snapshot) != nil || snapshot == nil {
		return nil, errors.New("invalid legacy usage snapshot JSON")
	}
	// Resolve known secrets before treating strings as labels or public IDs.
	keys := snapshot.APIs
	snapshot.APIs = make(map[string]APISnapshot, len(keys))
	for name, api := range keys {
		name = strings.TrimSpace(name)
		key := ""
		if row := GetAPIKey(name); row != nil {
			key = strings.TrimSpace(row.ID)
			if key == "" {
				key = MaskAPIKey(name)
			}
		} else {
			key = snapshotBucketKey(name)
		}
		addSnapshotAPI(snapshot.APIs, key, api, 1)
	}
	return json.Marshal(snapshot)
}

func (w *snapshotWriter) flush(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := w.save(ctx); err != nil && parent.Err() == nil {
		log.WithError(err).Warn("usage: failed to save PostgreSQL checkpoint")
	}
	// Also reaps locks in single-node installations, where the cluster janitor
	// is not started.
	_, _ = w.db.ExecContext(ctx, `DELETE FROM runtime_named_locks WHERE ctid IN (
		SELECT ctid FROM runtime_named_locks WHERE expires_at <= statement_timestamp()
		ORDER BY expires_at LIMIT 2000 FOR UPDATE SKIP LOCKED)`)
}

func (w *snapshotWriter) save(ctx context.Context) error {
	current := w.stats.Snapshot()
	delta := StatisticsSnapshot{}
	accumulateSnapshot(&delta, current, 1)
	accumulateSnapshot(&delta, w.saved, -1)
	if delta.TotalRequests == 0 && delta.TotalTokens == 0 {
		return nil
	}

	// During overlap, both holders start from the same saved history. Add only
	// this process's new counts to the latest row; never replace its history.
	tx, err := w.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM runtime_usage_snapshots WHERE node_id = $1 FOR UPDATE`,
		w.key).Scan(&data)
	if err != nil {
		return err
	}
	var persisted StatisticsSnapshot
	if err = json.Unmarshal(data, &persisted); err != nil {
		return err
	}
	accumulateSnapshot(&persisted, delta, 1)
	data, err = json.Marshal(persisted)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE runtime_usage_snapshots SET payload = $2, updated_at = clock_timestamp()
		WHERE node_id = $1`, w.key, data)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		w.saved = current
	}
	return err
}

func accumulateSnapshot(dst *StatisticsSnapshot, src StatisticsSnapshot, sign int64) {
	dst.TotalRequests += sign * src.TotalRequests
	dst.TotalTokens += sign * src.TotalTokens
	dst.SuccessCount += sign * src.SuccessCount
	dst.FailureCount += sign * src.FailureCount
	if dst.APIs == nil {
		dst.APIs = make(map[string]APISnapshot)
	}
	for key, api := range src.APIs {
		addSnapshotAPI(dst.APIs, key, api, sign)
	}
	accumulateCounts(&dst.RequestsByDay, src.RequestsByDay, sign)
	accumulateCounts(&dst.RequestsByHour, src.RequestsByHour, sign)
	accumulateCounts(&dst.TokensByDay, src.TokensByDay, sign)
	accumulateCounts(&dst.TokensByHour, src.TokensByHour, sign)
}

func addSnapshotAPI(apis map[string]APISnapshot, key string, value APISnapshot, sign int64) {
	api := apis[key]
	api.TotalRequests += sign * value.TotalRequests
	api.TotalTokens += sign * value.TotalTokens
	if api.Models == nil {
		api.Models = make(map[string]ModelSnapshot)
	}
	for name, model := range value.Models {
		entry := api.Models[name]
		entry.TotalRequests += sign * model.TotalRequests
		entry.TotalTokens += sign * model.TotalTokens
		api.Models[name] = entry
	}
	apis[key] = api
}

func accumulateCounts(dst *map[string]int64, src map[string]int64, sign int64) {
	if *dst == nil {
		*dst = make(map[string]int64)
	}
	for key, count := range src {
		(*dst)[key] += sign * count
	}
}

func (s *RequestStatistics) restoreSnapshot(snapshot StatisticsSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalRequests, s.successCount = snapshot.TotalRequests, snapshot.SuccessCount
	s.failureCount, s.totalTokens = snapshot.FailureCount, snapshot.TotalTokens
	s.apis = make(map[string]*apiStats, len(snapshot.APIs))
	for key, api := range snapshot.APIs {
		models := make(map[string]*modelStats, len(api.Models))
		for name, stats := range api.Models {
			models[name] = &modelStats{stats.TotalRequests, stats.TotalTokens}
		}
		s.apis[key] = &apiStats{api.TotalRequests, api.TotalTokens, models}
	}
	s.requestsByDay = make(map[string]int64)
	s.tokensByDay = make(map[string]int64)
	accumulateCounts(&s.requestsByDay, snapshot.RequestsByDay, 1)
	accumulateCounts(&s.tokensByDay, snapshot.TokensByDay, 1)
	s.requestsByHour = snapshotHours(snapshot.RequestsByHour)
	s.tokensByHour = snapshotHours(snapshot.TokensByHour)
}

func snapshotHours(byHour map[string]int64) map[int]int64 {
	hours := make(map[int]int64, len(byHour))
	for key, count := range byHour {
		hour, err := strconv.Atoi(key)
		if err == nil && hour >= 0 && hour < 24 {
			hours[hour] += count
		}
	}
	return hours
}
