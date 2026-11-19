package pgredis

import (
	"context"
	"time"
)

// RateLimitResult is one decision of a limiter. The fields mirror what a
// clustered quota needs to report: whether the request may proceed, how much
// budget is left, and when the window resets.
type RateLimitResult struct {
	Allowed   bool
	Limit     int64
	Used      int64
	Remaining int64
	ResetAt   time.Time
	// RetryAfter is how long the caller should wait before the next attempt is
	// expected to succeed. Zero when Allowed is true.
	RetryAfter time.Duration
}

// RateLimitSpec describes a fixed window.
type RateLimitSpec struct {
	// Window is the length of the fixed window. It must be positive.
	Window time.Duration
	// Limit is how many hits the window allows. A non-positive limit makes the
	// limiter a pure counter that never rejects.
	Limit int64
}

func (spec RateLimitSpec) validate() error {
	if spec.Window <= 0 {
		return &InvalidWindowError{Window: spec.Window, Limit: spec.Limit}
	}
	return nil
}

// RateLimitHit consumes one unit of budget and reports the decision.
//
// The window key is derived from the database clock, and the increment and the
// limit check happen in one statement: two nodes cannot both consume the last
// unit. A window row whose expiry has passed resets to 1 instead of continuing
// to count, so the window is closed without needing cleanup.
func (s *Store) RateLimitHit(ctx context.Context, key string, spec RateLimitSpec) (RateLimitResult, error) {
	return s.RateLimitHitN(ctx, key, 1, spec)
}

// RateLimitHitN consumes cost units. A cost larger than the limit is still
// counted, so an oversized request is refused and visibly expensive.
func (s *Store) RateLimitHitN(ctx context.Context, key string, cost int64, spec RateLimitSpec) (RateLimitResult, error) {
	if err := spec.validate(); err != nil {
		return RateLimitResult{}, err
	}
	if cost < 0 {
		cost = 0
	}
	windowMillis := spec.Window.Milliseconds()
	if windowMillis <= 0 {
		windowMillis = 1
	}
	var (
		used      int64
		expiresAt time.Time
		resetMS   int64
	)
	err := s.q.QueryRowContext(ctx,
		`INSERT INTO `+s.tables.rateLimit+`
		   (namespace, key, window_start, count, blocked_until, expires_at, updated_at)
		 VALUES (
		   $1, $2,
		   floor(extract(epoch FROM now()) * 1000 / $3::bigint)::bigint,
		   $4,
		   NULL,
		   now() + ($3::bigint * INTERVAL '1 millisecond'),
		   now()
		 )
		 ON CONFLICT (namespace, key, window_start) DO UPDATE
		    SET count = CASE
		                  WHEN `+s.tables.rateLimit+`.expires_at IS NOT NULL
		                   AND `+s.tables.rateLimit+`.expires_at <= now()
		                  THEN EXCLUDED.count
		                  ELSE `+s.tables.rateLimit+`.count + EXCLUDED.count
		                END,
		        expires_at = GREATEST(
		                       `+s.tables.rateLimit+`.expires_at,
		                       EXCLUDED.expires_at
		                     ),
		        updated_at = now()
		 RETURNING count,
		           expires_at,
		           GREATEST(0, floor(extract(epoch FROM (expires_at - now())) * 1000))::bigint`,
		s.ns(), key, windowMillis, cost).Scan(&used, &expiresAt, &resetMS)
	if err != nil {
		return RateLimitResult{}, err
	}
	result := RateLimitResult{
		Limit:     spec.Limit,
		Used:      used,
		Remaining: maxInt64(spec.Limit-used, 0),
		ResetAt:   expiresAt,
	}
	if spec.Limit <= 0 {
		result.Allowed = true
		return result, nil
	}
	result.Allowed = used <= spec.Limit
	if !result.Allowed {
		// ResetAt comes from the database clock, so RetryAfter is derived from
		// it rather than from the local clock, which may be skewed.
		result.Remaining = 0
		result.RetryAfter = time.Duration(resetMS) * time.Millisecond
	}
	return result, nil
}

// RateLimitPeek reads the current window without consuming budget.
func (s *Store) RateLimitPeek(ctx context.Context, key string, spec RateLimitSpec) (RateLimitResult, error) {
	if err := spec.validate(); err != nil {
		return RateLimitResult{}, err
	}
	windowMillis := spec.Window.Milliseconds()
	if windowMillis <= 0 {
		windowMillis = 1
	}
	var (
		used      int64
		expiresAt time.Time
	)
	err := s.q.QueryRowContext(ctx,
		`SELECT count, expires_at
		   FROM `+s.tables.rateLimit+`
		  WHERE namespace = $1 AND key = $2
		    AND window_start = floor(extract(epoch FROM now()) * 1000 / $3::bigint)::bigint
		    AND (expires_at IS NULL OR expires_at > now())`,
		s.ns(), key, windowMillis).Scan(&used, &expiresAt)
	if err != nil && !isNoRows(err) {
		return RateLimitResult{}, err
	}
	result := RateLimitResult{
		Limit:     spec.Limit,
		Used:      used,
		Remaining: maxInt64(spec.Limit-used, 0),
		ResetAt:   expiresAt,
	}
	result.Allowed = spec.Limit <= 0 || used < spec.Limit
	return result, nil
}

// RateLimitReward returns budget to the current window: a job that failed
// before doing any work should not burn a slot. It reports the resulting state
// and never goes below zero.
func (s *Store) RateLimitReward(ctx context.Context, key string, cost int64, spec RateLimitSpec) (RateLimitResult, error) {
	if err := spec.validate(); err != nil {
		return RateLimitResult{}, err
	}
	if cost < 0 {
		cost = 0
	}
	windowMillis := spec.Window.Milliseconds()
	if windowMillis <= 0 {
		windowMillis = 1
	}
	var (
		used      int64
		expiresAt time.Time
	)
	err := s.q.QueryRowContext(ctx,
		`UPDATE `+s.tables.rateLimit+`
		    SET count = GREATEST(count - $4::bigint, 0), updated_at = now()
		  WHERE namespace = $1 AND key = $2
		    AND window_start = floor(extract(epoch FROM now()) * 1000 / $3::bigint)::bigint
		    AND (expires_at IS NULL OR expires_at > now())
		 RETURNING count, expires_at`,
		s.ns(), key, windowMillis, cost).Scan(&used, &expiresAt)
	if err != nil {
		if isNoRows(err) {
			return RateLimitResult{Limit: spec.Limit, Allowed: true}, nil
		}
		return RateLimitResult{}, err
	}
	return RateLimitResult{
		Allowed:   spec.Limit <= 0 || used < spec.Limit,
		Limit:     spec.Limit,
		Used:      used,
		Remaining: maxInt64(spec.Limit-used, 0),
		ResetAt:   expiresAt,
	}, nil
}

// SweepRateLimits removes up to limit closed windows.
func (s *Store) SweepRateLimits(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	tag, err := s.q.ExecContext(ctx,
		`DELETE FROM `+s.tables.rateLimit+`
		  WHERE ctid IN (
		    SELECT ctid FROM `+s.tables.rateLimit+`
		     WHERE namespace = $1 AND expires_at IS NOT NULL AND expires_at <= now()
		     LIMIT $2
		  )`,
		s.ns(), limit)
	if err != nil {
		return 0, err
	}
	return rowsAffected(tag), nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
