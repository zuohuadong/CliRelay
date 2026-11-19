package usage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/pgredis"
)

// The aggregated usage statistics are held in memory and written to a shared
// store every 30 seconds, so a restart (or another node) resumes from the last
// snapshot instead of zero.
//
// Redis was the only implementation. A deployment that already runs PostgreSQL
// for the runtime data needs no second server for this: internal/pgredis puts
// the same key/value state in the runtime database.
type snapshotStore interface {
	// Name labels the store in log lines.
	Name() string
	// Save replaces the stored snapshot with data.
	Save(ctx context.Context, data []byte) error
	// Load returns the stored snapshot. found is false on a first run.
	Load(ctx context.Context) (data []byte, found bool, err error)
	// Close releases the client, flushing nothing.
	Close()
}

var usageSnapshotStore snapshotStore

// redisSnapshotStore keeps the snapshot in Redis, where it started.
type redisSnapshotStore struct {
	client *redis.Client
}

func (s *redisSnapshotStore) Name() string { return "Redis" }

func (s *redisSnapshotStore) Save(ctx context.Context, data []byte) error {
	return s.client.Set(ctx, redisUsageSnapshotKey(), data, 0).Err()
}

func (s *redisSnapshotStore) Load(ctx context.Context) ([]byte, bool, error) {
	data, err := s.client.Get(ctx, redisUsageSnapshotKey()).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s *redisSnapshotStore) Close() { _ = s.client.Close() }

// pgSnapshotStore keeps the snapshot in the runtime PostgreSQL database through
// internal/pgredis, so one database holds both the facts and this cache.
type pgSnapshotStore struct {
	store *pgredis.Store
}

func (s *pgSnapshotStore) Name() string { return "PostgreSQL" }

func (s *pgSnapshotStore) Save(ctx context.Context, data []byte) error {
	// RawMessage keeps the snapshot as JSON in the value column instead of
	// base64-encoding a []byte.
	return s.store.Set(ctx, redisUsageSnapshotKey(), json.RawMessage(data), 0)
}

func (s *pgSnapshotStore) Load(ctx context.Context) ([]byte, bool, error) {
	var raw json.RawMessage
	found, err := s.store.Get(ctx, redisUsageSnapshotKey(), &raw)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	return raw, true, nil
}

func (s *pgSnapshotStore) Close() {}

// InitPostgresSnapshot persists the usage snapshot in the runtime database
// instead of Redis. q is normally usage.RuntimeDB(), so no second pool and no
// second server are needed. It replaces a store installed by InitRedis.
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
	return nil
}
