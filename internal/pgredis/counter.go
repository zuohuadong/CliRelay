package pgredis

import "context"

// Counter is an integer cell keyed by string. All operations are single
// statements, so increments from different nodes never lose an update.
//
// The counter is what replaces INCR for rate accounting; see RateLimit for the
// windowed form and zset.go for the slot pool that needs the same atomicity
// with a capacity check.

// CounterAdd adds delta to key and returns the new value. A negative delta
// decrements. The key is created on first use; ttl only applies when the key is
// created, like INCR followed by EXPIRE.
func (s *Store) CounterAdd(ctx context.Context, key string, delta int64, ttl TTL) (int64, error) {
	var value int64
	err := s.q.QueryRowContext(ctx,
		`INSERT INTO `+s.tables.counter+` (namespace, key, value, expires_at, updated_at)
		 VALUES ($1, $2, $3, `+expiryExpr("$4")+`, now())
		 ON CONFLICT (namespace, key) DO UPDATE
		    SET value = CASE
		                  WHEN `+s.tables.counter+`.expires_at IS NOT NULL
		                   AND `+s.tables.counter+`.expires_at <= now()
		                  THEN EXCLUDED.value
		                  ELSE `+s.tables.counter+`.value + EXCLUDED.value
		                END,
		        expires_at = CASE
		                       WHEN `+s.tables.counter+`.expires_at IS NOT NULL
		                        AND `+s.tables.counter+`.expires_at <= now()
		                       THEN EXCLUDED.expires_at
		                       ELSE `+s.tables.counter+`.expires_at
		                     END,
		        updated_at = now()
		 RETURNING value`,
		s.ns(), key, delta, ttl.millis()).Scan(&value)
	return value, err
}

// CounterGet reads the current value. A missing or expired key reports 0 and
// found=false, which callers usually treat as zero.
func (s *Store) CounterGet(ctx context.Context, key string) (int64, bool, error) {
	var value int64
	err := s.q.QueryRowContext(ctx,
		`SELECT value FROM `+s.tables.counter+`
		  WHERE namespace = $1 AND key = $2
		    AND (expires_at IS NULL OR expires_at > now())`,
		s.ns(), key).Scan(&value)
	if err != nil {
		if isNoRows(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return value, true, nil
}

// CounterSet overwrites the value, which is useful for restores and tests.
func (s *Store) CounterSet(ctx context.Context, key string, value int64, ttl TTL) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO `+s.tables.counter+` (namespace, key, value, expires_at, updated_at)
		 VALUES ($1, $2, $3, `+expiryExpr("$4")+`, now())
		 ON CONFLICT (namespace, key) DO UPDATE
		    SET value = EXCLUDED.value,
		        expires_at = EXCLUDED.expires_at,
		        updated_at = now()`,
		s.ns(), key, value, ttl.millis())
	return err
}

// CounterDelete removes the key and reports whether it existed.
func (s *Store) CounterDelete(ctx context.Context, key string) (bool, error) {
	tag, err := s.q.ExecContext(ctx,
		`DELETE FROM `+s.tables.counter+` WHERE namespace = $1 AND key = $2`,
		s.ns(), key)
	if err != nil {
		return false, err
	}
	return rowsAffected(tag) > 0, nil
}

// SweepCounters removes up to limit expired rows.
func (s *Store) SweepCounters(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	tag, err := s.q.ExecContext(ctx,
		`DELETE FROM `+s.tables.counter+`
		  WHERE ctid IN (
		    SELECT ctid FROM `+s.tables.counter+`
		     WHERE namespace = $1 AND expires_at IS NOT NULL AND expires_at <= now()
		     LIMIT $2
		  )`,
		s.ns(), limit)
	if err != nil {
		return 0, err
	}
	return rowsAffected(tag), nil
}
