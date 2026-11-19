package pgredis

import (
	"context"
	"math"
	"time"
)

// ZMember is one sorted-set entry.
type ZMember struct {
	Member string
	Score  float64
}

// ZAdd upserts a member. The score is the only ordering key; a member that
// already exists moves to the new score.
func (s *Store) ZAdd(ctx context.Context, key, member string, score float64) error {
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return &InvalidScoreError{Score: score}
	}
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO `+s.tables.zset+` (namespace, key, member, score, updated_at)
		 VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (namespace, key, member) DO UPDATE
		    SET score = EXCLUDED.score, updated_at = now()`,
		s.ns(), key, member, score)
	return err
}

// ZScore reads one score. found=false covers both "absent" and "expired".
func (s *Store) ZScore(ctx context.Context, key, member string) (score float64, found bool, err error) {
	err = s.q.QueryRowContext(ctx,
		`SELECT score FROM `+s.tables.zset+`
		  WHERE namespace = $1 AND key = $2 AND member = $3
		    AND (expires_at IS NULL OR expires_at > now())`,
		s.ns(), key, member).Scan(&score)
	if err != nil {
		if isNoRows(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return score, true, nil
}

// ZCard counts live members.
func (s *Store) ZCard(ctx context.Context, key string) (int64, error) {
	var count int64
	err := s.q.QueryRowContext(ctx,
		`SELECT count(*) FROM `+s.tables.zset+`
		  WHERE namespace = $1 AND key = $2
		    AND (expires_at IS NULL OR expires_at > now())`,
		s.ns(), key).Scan(&count)
	return count, err
}

// ZRem deletes the named members and reports how many were live.
func (s *Store) ZRem(ctx context.Context, key string, members ...string) (int64, error) {
	if len(members) == 0 {
		return 0, nil
	}
	tag, err := s.q.ExecContext(ctx,
		`DELETE FROM `+s.tables.zset+`
		  WHERE namespace = $1 AND key = $2 AND member = ANY($3::text[])`,
		s.ns(), key, members)
	if err != nil {
		return 0, err
	}
	return rowsAffected(tag), nil
}

// ZRemRangeByScore deletes members whose score falls in [min, max]. This is the
// sweep that returns expired leases to the pool; it deliberately ignores
// expires_at, because an expired member still occupies a row.
func (s *Store) ZRemRangeByScore(ctx context.Context, key string, min, max float64) (int64, error) {
	tag, err := s.q.ExecContext(ctx,
		`DELETE FROM `+s.tables.zset+`
		  WHERE namespace = $1 AND key = $2 AND score >= $3 AND score <= $4`,
		s.ns(), key, min, max)
	if err != nil {
		return 0, err
	}
	return rowsAffected(tag), nil
}

// ZRange reads live members in score order, lowest first.
func (s *Store) ZRange(ctx context.Context, key string, offset, limit int) ([]ZMember, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.q.QueryContext(ctx,
		`SELECT member, score FROM `+s.tables.zset+`
		  WHERE namespace = $1 AND key = $2
		    AND (expires_at IS NULL OR expires_at > now())
		  ORDER BY score ASC, member ASC
		  OFFSET $3 LIMIT $4`,
		s.ns(), key, offset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ZMember
	for rows.Next() {
		var item ZMember
		if err := rows.Scan(&item.Member, &item.Score); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SlotReservation is the outcome of TryReserveSlot.
type SlotReservation struct {
	// Taken reports whether the caller now holds the slot. False means the pool
	// was full; the caller should fall back or reject.
	Taken bool
	// Held is the number of slots in use after the attempt, including this one
	// when Taken.
	Held int
}

// TryReserveSlot takes one slot in an expiring pool, atomically.
//
// This is the PostgreSQL replacement for the Redis lease script that the
// cluster quota used: ZREMRANGEBYSCORE (drop expired), ZCARD (count), ZADD
// (take), all inside one EVAL. Here it is one statement, so two nodes can never
// both observe "one slot left" and both take it.
//
// retention is how long the slot survives without renewal; a crashed holder
// leaks its slot for at most that long. The score is the expiry as a unix
// timestamp, which is also what makes the prune a score range.
func (s *Store) TryReserveSlot(ctx context.Context, key, member string, limit int, retention time.Duration) (SlotReservation, error) {
	if retention <= 0 {
		return SlotReservation{}, &InvalidRetentionError{Retention: retention}
	}
	now := float64(s.clock().UnixNano()) / 1e9
	expiry := float64(s.clock().Add(retention).UnixNano()) / 1e9
	// A non-positive limit means "unbounded": always take, never count.
	row := s.q.QueryRowContext(ctx,
		`WITH pruned AS (
		   DELETE FROM `+s.tables.zset+`
		    WHERE namespace = $1 AND key = $2 AND score <= $3
		    RETURNING member
		 ), held AS (
		   SELECT count(*)::int AS n
		     FROM `+s.tables.zset+`
		    WHERE namespace = $1 AND key = $2 AND score > $3 AND member <> $4
		 ), taken AS (
		   INSERT INTO `+s.tables.zset+` (namespace, key, member, score)
		   SELECT $1, $2, $4, $5
		    WHERE $6::int <= 0
		       OR (SELECT n FROM held) < $6::int
		       OR EXISTS (
		            SELECT 1 FROM `+s.tables.zset+`
		             WHERE namespace = $1 AND key = $2 AND member = $4 AND score > $3
		          )
		   ON CONFLICT (namespace, key, member) DO UPDATE
		      SET score = EXCLUDED.score, updated_at = now()
		   RETURNING member
		 )
		 SELECT COALESCE((SELECT n FROM held), 0) + (SELECT count(*)::int FROM taken) AS held,
		        EXISTS (SELECT 1 FROM taken) AS taken`,
		s.ns(), key, now, member, expiry, limit)
	var reservation SlotReservation
	if err := row.Scan(&reservation.Held, &reservation.Taken); err != nil {
		return SlotReservation{}, err
	}
	return reservation, nil
}

// RenewSlot extends an existing slot's expiry. It reports false when the slot
// is gone, which is how a holder learns it lost the slot while it was busy.
func (s *Store) RenewSlot(ctx context.Context, key, member string, retention time.Duration) (bool, error) {
	if retention <= 0 {
		return false, &InvalidRetentionError{Retention: retention}
	}
	now := s.clock()
	tag, err := s.q.ExecContext(ctx,
		`UPDATE `+s.tables.zset+`
		    SET score = $4, updated_at = now()
		  WHERE namespace = $1 AND key = $2 AND member = $3 AND score > $5`,
		s.ns(), key, member, float64(now.Add(retention).UnixNano())/1e9, float64(now.UnixNano())/1e9)
	if err != nil {
		return false, err
	}
	return rowsAffected(tag) > 0, nil
}

// ReleaseSlot drops one slot. Releasing a slot the caller no longer holds is
// not an error, so a late release cannot free someone else's slot.
func (s *Store) ReleaseSlot(ctx context.Context, key, member string) (bool, error) {
	removed, err := s.ZRem(ctx, key, member)
	return removed > 0, err
}

// Holders lists the live members with their expiry, for diagnostics.
func (s *Store) Holders(ctx context.Context, key string) ([]ZMember, error) {
	return s.ZRange(ctx, key, 0, 1000)
}
