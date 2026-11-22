package sharedstate

import (
	"context"
	"database/sql"
	"time"
)

type ThrottleWindow struct {
	Limit  int
	Window time.Duration
	Slots  int
}

type ThrottleSpec struct {
	Short      ThrottleWindow
	Long       ThrottleWindow
	Backoff    []time.Duration
	ResetAfter time.Duration
	Retention  time.Duration
}

type ThrottleState struct {
	Blocked    bool
	RetryAfter time.Duration
	Generation int
	ShortCount int
	LongCount  int
	NewlyArmed bool
}

type throttleValue struct {
	Short        timeBuckets `json:"short,omitempty"`
	Long         timeBuckets `json:"long,omitempty"`
	BlockedUntil time.Time   `json:"blocked_until"`
	LastActivity time.Time   `json:"last_activity"`
	Generation   int         `json:"generation"`
}

type timeBuckets map[int64]int

func (b *timeBuckets) count(now time.Time, w ThrottleWindow, charge bool) int {
	if w.Limit <= 0 || w.Window <= 0 || w.Slots <= 0 {
		return 0
	}
	width := max(time.Millisecond, w.Window/time.Duration(w.Slots)).Milliseconds()
	current := now.UnixMilli() / width
	oldest := current - int64(w.Slots) + 1
	if charge {
		if *b == nil {
			*b = make(timeBuckets)
		}
		(*b)[current]++
	}
	total := 0
	for index, count := range *b {
		if index >= oldest && index <= current {
			total += count
		} else if charge && index < oldest {
			delete(*b, index)
		}
	}
	return total
}

func throttleKey(bucket string) string { return stateKey("throttle", bucket) }

func (s *Store) ThrottleCharge(ctx context.Context, bucket string, spec ThrottleSpec, _ time.Time) (ThrottleState, error) {
	return s.throttle(ctx, bucket, spec, true)
}

func (s *Store) ThrottlePeek(ctx context.Context, bucket string, spec ThrottleSpec, _ time.Time) (ThrottleState, error) {
	if !s.Available() {
		return ThrottleState{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	now, err := s.databaseTime(ctx, s.db)
	if err != nil {
		return ThrottleState{}, err
	}
	row, err := loadKV[throttleValue](ctx, s.db, throttleKey(bucket), now)
	if err != nil || !row.Value.BlockedUntil.After(now) {
		return ThrottleState{}, err
	}
	return throttleStanding(row.Value, spec, now, false), nil
}

func (s *Store) ThrottleClear(ctx context.Context, bucket string) error {
	key := throttleKey(bucket)
	return s.transaction(ctx, key, func(ctx context.Context, tx *sql.Tx, _ time.Time) error {
		return deleteKV(ctx, tx, key)
	})
}

func throttleStanding(v throttleValue, spec ThrottleSpec, now time.Time, charge bool) ThrottleState {
	return ThrottleState{
		Blocked: v.BlockedUntil.After(now), RetryAfter: max(0, v.BlockedUntil.Sub(now)),
		Generation: v.Generation, ShortCount: v.Short.count(now, spec.Short, charge),
		LongCount: v.Long.count(now, spec.Long, charge),
	}
}

func (s *Store) throttle(ctx context.Context, bucket string, spec ThrottleSpec, charge bool) (ThrottleState, error) {
	key := throttleKey(bucket)
	var out ThrottleState
	err := s.transaction(ctx, key, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
		row, err := loadKV[throttleValue](ctx, tx, key, now)
		if err != nil {
			return err
		}
		v := &row.Value
		if spec.ResetAfter > 0 && now.Sub(v.LastActivity) >= spec.ResetAfter {
			*v = throttleValue{}
		}
		v.LastActivity = now
		blocked := v.BlockedUntil.After(now)
		out = throttleStanding(*v, spec, now, false)
		if charge && !blocked {
			out.ShortCount = v.Short.count(now, spec.Short, true)
			out.LongCount = v.Long.count(now, spec.Long, true)
			tripped := (spec.Short.Limit > 0 && out.ShortCount >= spec.Short.Limit) ||
				(spec.Long.Limit > 0 && out.LongCount >= spec.Long.Limit)
			if tripped && len(spec.Backoff) > 0 {
				if wait := spec.Backoff[min(v.Generation, len(spec.Backoff)-1)]; wait > 0 {
					v.Generation++
					v.BlockedUntil = now.Add(wait)
					out.Blocked, out.NewlyArmed, out.RetryAfter, out.Generation = true, true, wait, v.Generation
				}
			}
		}
		row.ExpiresAt = now.Add(max(spec.Retention, out.RetryAfter))
		return saveKV(ctx, tx, key, row)
	})
	return out, err
}
