package pgredis

import (
	"context"
	"errors"
	"time"
)

// ErrNotSet reports a conditional write that the condition rejected: SET NX on
// an existing key, SET XX on a missing one, or a compare-and-swap mismatch.
// It is a normal answer, not a failure.
var ErrNotSet = errors.New("pgredis: condition not met")

// TTL is a relative expiry. Zero or negative means "no expiry".
type TTL time.Duration

func (t TTL) millis() int64 {
	return time.Duration(t).Milliseconds()
}

// KVPutOptions tunes a Put.
type KVPutOptions struct {
	// TTL expires the key. Zero keeps it forever.
	TTL TTL
	// NX stores only when the key is absent or expired.
	NX bool
	// XX stores only when the key exists and has not expired.
	XX bool
}

// Get returns the stored JSON value decoded into v, or false when the key is
// absent or expired.
//
// A read never writes: an expired row stays in the table until a write or a
// sweep removes it, which keeps GET off the WAL and off row locks.
func (s *Store) Get(ctx context.Context, key string, v any) (bool, error) {
	row := s.q.QueryRowContext(ctx,
		`SELECT value
		   FROM `+s.tables.kv+`
		  WHERE namespace = $1 AND key = $2
		    AND (expires_at IS NULL OR expires_at > now())`,
		s.ns(), key)
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, err
	}
	if v == nil {
		return true, nil
	}
	return true, jsonUnmarshal(raw, v)
}

// Put stores v under key and reports whether the row was written. A false
// result means the NX/XX condition rejected the write; the error is nil.
func (s *Store) Put(ctx context.Context, key string, v any, opts KVPutOptions) (bool, error) {
	if opts.NX && opts.XX {
		return false, errors.New("pgredis: NX and XX are mutually exclusive")
	}
	payload, err := jsonMarshal(v)
	if err != nil {
		return false, err
	}
	// One statement covers every combination: the guard decides whether the
	// conflict target updates, so NX and XX need no separate SQL.
	row := s.q.QueryRowContext(ctx,
		// The SELECT guard skips the insert for XX, which has nothing to update;
		// the DO UPDATE guard decides whether an existing row may be replaced.
		`INSERT INTO `+s.tables.kv+` (namespace, key, value, expires_at, updated_at)
		 SELECT $1, $2, $3::jsonb, `+expiryExpr("$4")+`, now()
		  WHERE NOT $6::boolean
		 ON CONFLICT (namespace, key) DO UPDATE
		    SET value = EXCLUDED.value,
		        expires_at = EXCLUDED.expires_at,
		        updated_at = now()
		  WHERE CASE
		          WHEN $5::boolean THEN `+s.tables.kv+`.expires_at IS NOT NULL AND `+s.tables.kv+`.expires_at <= now()
		          WHEN $6::boolean THEN `+s.tables.kv+`.expires_at IS NULL OR `+s.tables.kv+`.expires_at > now()
		          ELSE TRUE
		        END
		 RETURNING key`,
		s.ns(), key, payload, opts.TTL.millis(), opts.NX, opts.XX)
	var written string
	if err := row.Scan(&written); err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Set is Put without conditions.
func (s *Store) Set(ctx context.Context, key string, v any, ttl TTL) error {
	_, err := s.Put(ctx, key, v, KVPutOptions{TTL: ttl})
	return err
}

// Delete removes a key and reports whether a live row was removed.
func (s *Store) Delete(ctx context.Context, key string) (bool, error) {
	tag, err := s.q.ExecContext(ctx,
		`DELETE FROM `+s.tables.kv+` WHERE namespace = $1 AND key = $2`,
		s.ns(), key)
	if err != nil {
		return false, err
	}
	return rowsAffected(tag) > 0, nil
}

// CompareAndSwapEqual swaps the value only when the stored JSON equals old.
// The comparison and the write are one statement, so no other writer can
// interleave between them.
func (s *Store) CompareAndSwapEqual(ctx context.Context, key string, old, v any, ttl TTL) (bool, error) {
	expected, err := jsonMarshal(old)
	if err != nil {
		return false, err
	}
	payload, err := jsonMarshal(v)
	if err != nil {
		return false, err
	}
	row := s.q.QueryRowContext(ctx,
		`UPDATE `+s.tables.kv+`
		    SET value = $4::jsonb,
		        expires_at = `+expiryExpr("$5")+`,
		        updated_at = now()
		  WHERE namespace = $1 AND key = $2
		    AND (expires_at IS NULL OR expires_at > now())
		    AND value = $3::jsonb
		 RETURNING key`,
		s.ns(), key, expected, payload, ttl.millis())
	var swapped string
	if err := row.Scan(&swapped); err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Expire sets or replaces the TTL. It reports whether a live row was updated.
func (s *Store) Expire(ctx context.Context, key string, ttl TTL) (bool, error) {
	tag, err := s.q.ExecContext(ctx,
		`UPDATE `+s.tables.kv+`
		    SET expires_at = `+expiryExpr("$3")+`, updated_at = now()
		  WHERE namespace = $1 AND key = $2
		    AND (expires_at IS NULL OR expires_at > now())`,
		s.ns(), key, ttl.millis())
	if err != nil {
		return false, err
	}
	return rowsAffected(tag) > 0, nil
}

// Persist clears the TTL. It reports whether a live row was updated.
func (s *Store) Persist(ctx context.Context, key string) (bool, error) {
	tag, err := s.q.ExecContext(ctx,
		`UPDATE `+s.tables.kv+`
		    SET expires_at = NULL, updated_at = now()
		  WHERE namespace = $1 AND key = $2
		    AND (expires_at IS NULL OR expires_at > now())`,
		s.ns(), key)
	if err != nil {
		return false, err
	}
	return rowsAffected(tag) > 0, nil
}

// SweepKV removes up to limit expired rows and reports how many went. Reads do
// not need it; a periodic call keeps the table from growing without bound.
func (s *Store) SweepKV(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	tag, err := s.q.ExecContext(ctx,
		`DELETE FROM `+s.tables.kv+`
		  WHERE ctid IN (
		    SELECT ctid FROM `+s.tables.kv+`
		     WHERE namespace = $1 AND expires_at IS NOT NULL AND expires_at <= now()
		     LIMIT $2
		  )`,
		s.ns(), limit)
	if err != nil {
		return 0, err
	}
	return rowsAffected(tag), nil
}

// expiryExpr renders "now() + n milliseconds" for a bigint parameter, or NULL
// when the parameter is zero or negative.
func expiryExpr(param string) string {
	return `CASE WHEN ` + param + `::bigint > 0
	             THEN now() + (` + param + `::bigint * INTERVAL '1 millisecond')
	             ELSE NULL END`
}
