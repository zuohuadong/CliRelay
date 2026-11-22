package home

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	postgresstore "github.com/router-for-me/CLIProxyAPI/v8/internal/storage/postgres"
)

// postgresKV is the PostgreSQL replacement for Home's Redis-style KV surface.
// The remote Home control-plane protocol remains a compatibility boundary, but
// caches and replay state used through CurrentKVClient never depend on Redis.
type postgresKV struct{ db *sql.DB }

var _ kvstore.KVClient = (*postgresKV)(nil)

func currentPostgresKV() (*postgresKV, bool) {
	db := postgresstore.RuntimeHandle()
	if db == nil {
		return nil, false
	}
	return &postgresKV{db: db}, true
}

func (s *postgresKV) KVGet(ctx context.Context, key string) ([]byte, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, false, errors.New("home postgres kv: key is empty")
	}
	var value []byte
	var expires sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT payload, expires_at FROM home_runtime_kv
		WHERE state_key = $1`, key).Scan(&value, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if expires.Valid && !expires.Time.After(time.Now()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM home_runtime_kv WHERE state_key = $1`, key)
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func ttlExpires(ttl time.Duration) any {
	if ttl <= 0 {
		return nil
	}
	return time.Now().Add(ttl)
}

func (s *postgresKV) KVSet(ctx context.Context, key string, value []byte, opts kvstore.KVSetOptions) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || (opts.NX && opts.XX) || (opts.EX > 0 && opts.PX > 0) || opts.EX < 0 || opts.PX < 0 {
		return false, errors.New("home postgres kv: invalid SET options")
	}
	ttl := opts.EX
	if opts.PX > 0 {
		ttl = opts.PX
	}
	expires := ttlExpires(ttl)
	var result sql.Result
	var err error
	switch {
	case opts.NX:
		result, err = s.db.ExecContext(ctx, `INSERT INTO home_runtime_kv (state_key, payload, expires_at)
			VALUES ($1, $2, $3) ON CONFLICT (state_key) DO NOTHING`, key, value, expires)
	case opts.XX:
		result, err = s.db.ExecContext(ctx, `UPDATE home_runtime_kv SET payload = $2, expires_at = $3
			WHERE state_key = $1 AND (expires_at IS NULL OR expires_at > clock_timestamp())`, key, value, expires)
	default:
		result, err = s.db.ExecContext(ctx, `INSERT INTO home_runtime_kv (state_key, payload, expires_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (state_key) DO UPDATE SET payload = EXCLUDED.payload, expires_at = EXCLUDED.expires_at`,
			key, value, expires)
	}
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *postgresKV) KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return s.KVSet(ctx, key, value, kvstore.KVSetOptions{NX: true, PX: ttl})
}

func (s *postgresKV) KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 901217))`, key); err != nil {
		return false, err
	}
	var current []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM home_runtime_kv
		WHERE state_key = $1 AND (expires_at IS NULL OR expires_at > clock_timestamp()) FOR UPDATE`, key).Scan(&current)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if found != expectedExists || (found && !bytes.Equal(current, expected)) {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO home_runtime_kv (state_key, payload, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (state_key) DO UPDATE SET payload = EXCLUDED.payload, expires_at = EXCLUDED.expires_at`,
		key, value, ttlExpires(ttl))
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *postgresKV) KVDel(ctx context.Context, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	var deleted int64
	for _, key := range keys {
		result, err := s.db.ExecContext(ctx, `DELETE FROM home_runtime_kv WHERE state_key = $1`, strings.TrimSpace(key))
		if err != nil {
			return deleted, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += n
	}
	return deleted, nil
}

func (s *postgresKV) KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE home_runtime_kv SET expires_at = $2
		WHERE state_key = $1 AND (expires_at IS NULL OR expires_at > clock_timestamp())`, key, ttlExpires(ttl))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *postgresKV) KVTTL(ctx context.Context, key string) (time.Duration, bool, error) {
	var expires sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM home_runtime_kv
		WHERE state_key = $1 AND (expires_at IS NULL OR expires_at > clock_timestamp())`, key).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !expires.Valid {
		return 0, true, nil
	}
	return time.Until(expires.Time), true, nil
}

func (s *postgresKV) KVIncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 901217))`, key); err != nil {
		return 0, err
	}
	var value []byte
	_ = tx.QueryRowContext(ctx, `SELECT payload FROM home_runtime_kv WHERE state_key = $1
		AND (expires_at IS NULL OR expires_at > clock_timestamp()) FOR UPDATE`, key).Scan(&value)
	var current int64
	if len(value) > 0 {
		if _, err = fmt.Sscan(string(value), &current); err != nil {
			return 0, err
		}
	}
	current += delta
	if _, err = tx.ExecContext(ctx, `INSERT INTO home_runtime_kv (state_key, payload, expires_at)
		VALUES ($1, $2, NULL) ON CONFLICT (state_key) DO UPDATE SET payload = EXCLUDED.payload, expires_at = NULL`,
		key, []byte(strconv.FormatInt(current, 10))); err != nil {
		return 0, err
	}
	return current, tx.Commit()
}

func (s *postgresKV) KVMGet(ctx context.Context, keys ...string) ([][]byte, []bool, error) {
	values := make([][]byte, len(keys))
	found := make([]bool, len(keys))
	for i, key := range keys {
		values[i], found[i], _ = s.KVGet(ctx, key)
	}
	return values, found, nil
}

func (s *postgresKV) KVMSet(ctx context.Context, pairs map[string][]byte) error {
	for key, value := range pairs {
		if _, err := s.KVSet(ctx, key, value, kvstore.KVSetOptions{}); err != nil {
			return err
		}
	}
	return nil
}
