package usage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	pgredis "github.com/zuohuadong/pgredis-go"
)

// usageSnapshotBaseKey names the single value holding the aggregate. The
// historical name is kept so an existing deployment resumes from the snapshot
// it already has.
const usageSnapshotBaseKey = "cliproxy:usage_stats_snapshot"

// The aggregated usage statistics are held in memory and written to the
// runtime database every 30 seconds, so a restart (or another node) resumes
// from the last snapshot instead of zero.
type snapshotStore interface {
	// Name labels the store in log lines.
	Name() string
	// Save replaces the stored snapshot with data.
	Save(ctx context.Context, data []byte) error
	// Load returns the stored snapshot. found is false on a first run.
	Load(ctx context.Context) (data []byte, found bool, err error)
	// Close releases the store, flushing nothing.
	Close()
}

var usageSnapshotStore snapshotStore

// pgSnapshotStore keeps the snapshot in the runtime PostgreSQL database through
// internal/pgredis, so one database holds both the facts and this cache.
type pgSnapshotStore struct {
	store *pgredis.Store
}

func (s *pgSnapshotStore) Name() string { return "PostgreSQL" }

func (s *pgSnapshotStore) Save(ctx context.Context, data []byte) error {
	// RawMessage keeps the snapshot as JSON in the value column instead of
	// base64-encoding a []byte.
	return s.store.Set(ctx, usageSnapshotKey(), json.RawMessage(data), 0)
}

func (s *pgSnapshotStore) Load(ctx context.Context) ([]byte, bool, error) {
	var raw json.RawMessage
	found, err := s.store.Get(ctx, usageSnapshotKey(), &raw)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	return raw, true, nil
}

func (s *pgSnapshotStore) Close() {}

// InitPostgresSnapshot persists the usage snapshot in the runtime database.
// q is normally usage.RuntimeDB(), so no second pool and no second server are
// needed. It installs the store, loads what a previous process saved and starts
// the periodic save loop.
func InitPostgresSnapshot(q pgredis.Querier) error {
	if q == nil {
		return errors.New("usage: postgres snapshot store needs a database handle")
	}
	store := pgredis.New(q, pgredis.Options{Namespace: "usage", TablePrefix: "pgredis"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.EnsureSchema(ctx); err != nil {
		return err
	}
	usageSnapshotStore = &pgSnapshotStore{store: store}
	StartSnapshotLoop()
	return nil
}
