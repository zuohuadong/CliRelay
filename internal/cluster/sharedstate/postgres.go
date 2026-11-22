package sharedstate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const (
	requestTimeout    = 200 * time.Millisecond
	backgroundTimeout = 2 * time.Second
)

var ErrUnavailable = errors.New("cluster shared PostgreSQL state unavailable")

func stateKey(namespace string, parts ...string) string {
	hash := sha256.New()
	var size [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(part))
	}
	return namespace + ":" + hex.EncodeToString(hash.Sum(nil))
}

func (s *Store) transaction(ctx context.Context, scope string, op func(context.Context, *sql.Tx, time.Time) error) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return s.transactionScopes(ctx, []string{scope}, op)
}

// Multiple scopes must be ordered consistently to avoid deadlocks.
func (s *Store) transactionScopes(ctx context.Context, scopes []string, op func(context.Context, *sql.Tx, time.Time) error) error {
	if !s.Available() {
		return ErrUnavailable
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A row may not exist (or may expire). Serialize mutations of both the
	// empty state and its live replacement on the same transaction lock.
	for _, scope := range scopes {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 714173))`, scope); err != nil {
			return err
		}
	}
	now, err := s.databaseTime(ctx, tx)
	if err != nil {
		return err
	}
	if err = op(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

type timeQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) databaseTime(ctx context.Context, db timeQuery) (time.Time, error) {
	if s.now != nil {
		return s.now(), nil
	}
	var now time.Time
	err := db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

type kvRecord[T any] struct {
	Value     T
	ExpiresAt time.Time
	Found     bool
}

func loadKV[T any](ctx context.Context, db timeQuery, key string, now time.Time) (kvRecord[T], error) {
	var record kvRecord[T]
	var payload []byte
	err := db.QueryRowContext(ctx, `
		SELECT payload, expires_at FROM cluster_runtime_kv
		WHERE state_key = $1 AND expires_at > $2`, key, now).Scan(&payload, &record.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	record.Found = true
	return record, json.Unmarshal(payload, &record.Value)
}

func saveKV[T any](ctx context.Context, tx *sql.Tx, key string, record kvRecord[T]) error {
	payload, err := json.Marshal(record.Value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO cluster_runtime_kv (state_key, payload, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (state_key) DO UPDATE
		SET payload = EXCLUDED.payload, expires_at = EXCLUDED.expires_at`, key, payload, record.ExpiresAt)
	return err
}

func deleteKV(ctx context.Context, tx *sql.Tx, key string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM cluster_runtime_kv WHERE state_key = $1`, key)
	return err
}

func (s *Store) cleanupLoop(ctx context.Context) {
	defer close(s.cleanupDone)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupExpired(ctx)
		}
	}
}

func (s *Store) cleanupExpired(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, backgroundTimeout)
	defer cancel()
	for _, table := range []string{"cluster_runtime_kv", "cluster_runtime_leases", "cluster_runtime_rates", "runtime_named_locks"} {
		for ctx.Err() == nil {
			result, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE ctid IN (
				SELECT ctid FROM `+table+` WHERE expires_at <= statement_timestamp()
				ORDER BY expires_at LIMIT 2000 FOR UPDATE SKIP LOCKED
			)`)
			if err != nil {
				break
			}
			if n, err := result.RowsAffected(); err != nil || n < 2000 {
				break
			}
		}
	}
}
