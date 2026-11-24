package sharedstate

import (
	"context"
	"fmt"
	"strconv"
	"time"

	pgredis "github.com/zuohuadong/pgredis-go"
)

// Login throttle buckets in PostgreSQL.
//
// The decision is taken in Go on a snapshot of the bucket and written back with
// compare-and-swap, so a failure that loses a race re-reads and retries instead
// of going uncounted.
// The state machine itself (sub-buckets, quiet-period reset, refusal while
// blocked, escalating block ladder) is the Go copy below, and it is what the
// tests exercise without a database.
//
// A bucket is one KV row holding the whole state as JSON, so a charge is one
// read and one write. That is only acceptable because login failures are rare
// per bucket: a guessing run against one account is the worst case, and even
// then the retry loop bounds the cost, with the node-local throttle as the
// fallback when the row is too contended to update.

// throttleRetries bounds the compare-and-swap loop for one charge.
const throttleRetries = 24

// throttleBucketState is the JSON payload of one bucket. The short field names
// keep the stored value small.
type throttleBucketState struct {
	Short        map[string]int `json:"s,omitempty"`
	Long         map[string]int `json:"l,omitempty"`
	BlockedUntil int64          `json:"bu,omitempty"`
	Generation   int            `json:"g,omitempty"`
	LastActivity int64          `json:"la,omitempty"`
}

// pgThrottleKey names the bucket's row.
func pgThrottleKey(bucket string) string {
	return "throttle:" + HashPart(bucket)
}

// ThrottleCharge charges one failure against bucket.
func (s *PGStore) ThrottleCharge(ctx context.Context, bucket string, spec ThrottleSpec, now time.Time) (ThrottleState, error) {
	if !s.Available() {
		return ThrottleState{}, ErrUnavailable
	}
	key := pgThrottleKey(bucket)
	for attempt := 0; attempt < throttleRetries; attempt++ {
		var state throttleBucketState
		found, err := s.store.Get(ctx, key, &state)
		if err != nil {
			return ThrottleState{}, err
		}
		// The comparison below needs the value that was read, so the advance
		// works on a copy: the sub-bucket maps are mutated in place.
		next, standing, ttl := throttleAdvance(state.clone(), true, spec, now)
		var swapped bool
		if found {
			swapped, err = s.store.CompareAndSwapEqual(ctx, key, state, next, pgredis.TTL(ttl))
		} else {
			swapped, err = s.store.Put(ctx, key, next, pgredis.KVPutOptions{TTL: pgredis.TTL(ttl), NX: true})
		}
		if err != nil {
			return ThrottleState{}, err
		}
		if swapped {
			return standing, nil
		}
		// Another node charged the same bucket first: retry on its result.
	}
	return ThrottleState{}, fmt.Errorf("sharedstate: throttle bucket %q is too contended to update", bucket)
}

// ThrottlePeek reports a bucket's standing without charging it.
func (s *PGStore) ThrottlePeek(ctx context.Context, bucket string, spec ThrottleSpec, now time.Time) (ThrottleState, error) {
	if !s.Available() {
		return ThrottleState{}, ErrUnavailable
	}
	var state throttleBucketState
	if _, err := s.store.Get(ctx, pgThrottleKey(bucket), &state); err != nil {
		return ThrottleState{}, err
	}
	_, standing, _ := throttleAdvance(state, false, spec, now)
	return standing, nil
}

// ThrottleClear deletes a bucket, as a successful login does.
func (s *PGStore) ThrottleClear(ctx context.Context, bucket string) error {
	if !s.Available() {
		return ErrUnavailable
	}
	_, err := s.store.Delete(ctx, pgThrottleKey(bucket))
	return err
}

// clone copies the state so a charge can mutate its maps without disturbing the
// value that was read.
func (s throttleBucketState) clone() throttleBucketState {
	out := s
	if s.Short != nil {
		out.Short = make(map[string]int, len(s.Short))
		for k, v := range s.Short {
			out.Short[k] = v
		}
	}
	if s.Long != nil {
		out.Long = make(map[string]int, len(s.Long))
		for k, v := range s.Long {
			out.Long[k] = v
		}
	}
	return out
}

// throttleAdvance applies one charge or peek and reports the resulting state,
// the standing, and the TTL that keeps the row until it is next needed.
func throttleAdvance(state throttleBucketState, charge bool, spec ThrottleSpec, now time.Time) (throttleBucketState, ThrottleState, time.Duration) {
	nowMs := now.UnixMilli()
	if charge {
		if after := spec.ResetAfter.Milliseconds(); after > 0 && state.LastActivity > 0 && nowMs-state.LastActivity >= after {
			// A quiet bucket starts over: the run is considered abandoned.
			state = throttleBucketState{}
		}
	}

	if !charge {
		if state.BlockedUntil > nowMs {
			return state, ThrottleState{
				Blocked:    true,
				RetryAfter: time.Duration(state.BlockedUntil-nowMs) * time.Millisecond,
				Generation: state.Generation,
				ShortCount: throttleCount(state.Short, spec.Short, nowMs),
				LongCount:  throttleCount(state.Long, spec.Long, nowMs),
			}, 0
		}
		return state, ThrottleState{}, 0
	}

	state.LastActivity = nowMs
	if state.BlockedUntil > nowMs {
		// Failures arriving while a block is armed are refused without being
		// counted, so a client hammering a closed door neither extends nor
		// escalates the block.
		retry := time.Duration(state.BlockedUntil-nowMs) * time.Millisecond
		return state, ThrottleState{
			Blocked:    true,
			RetryAfter: retry,
			Generation: state.Generation,
			ShortCount: throttleCount(state.Short, spec.Short, nowMs),
			LongCount:  throttleCount(state.Long, spec.Long, nowMs),
		}, throttleKeep(spec.Retention, retry)
	}

	state.Short = throttleChargeWindow(state.Short, spec.Short, nowMs)
	state.Long = throttleChargeWindow(state.Long, spec.Long, nowMs)
	shortCount := throttleCount(state.Short, spec.Short, nowMs)
	longCount := throttleCount(state.Long, spec.Long, nowMs)
	tripped := (spec.Short.Limit > 0 && shortCount >= spec.Short.Limit) ||
		(spec.Long.Limit > 0 && longCount >= spec.Long.Limit)
	standing := ThrottleState{Generation: state.Generation, ShortCount: shortCount, LongCount: longCount}
	if !tripped || len(spec.Backoff) < 1 {
		return state, standing, throttleKeep(spec.Retention, 0)
	}
	rung := state.Generation + 1
	if rung > len(spec.Backoff) {
		rung = len(spec.Backoff)
	}
	wait := spec.Backoff[rung-1]
	if wait <= 0 {
		// The ladder's last rung can be zero, meaning "stop blocking".
		return state, standing, throttleKeep(spec.Retention, 0)
	}
	state.Generation++
	state.BlockedUntil = nowMs + wait.Milliseconds()
	standing.Blocked = true
	standing.RetryAfter = wait
	standing.Generation = state.Generation
	standing.NewlyArmed = true
	return state, standing, throttleKeep(spec.Retention, wait)
}

// throttleChargeWindow counts one failure in the window's current sub-bucket
// and drops sub-buckets that fell out of it.
func throttleChargeWindow(counts map[string]int, window ThrottleWindow, nowMs int64) map[string]int {
	width := slotWidth(window)
	if width <= 0 {
		return counts
	}
	current := nowMs / width.Milliseconds()
	field := strconv.FormatInt(current, 10)
	if counts == nil {
		counts = make(map[string]int, window.Slots)
	}
	counts[field]++
	oldest := current - int64(window.Slots) + 1
	for name := range counts {
		index, err := strconv.ParseInt(name, 10, 64)
		if err == nil && index < oldest {
			delete(counts, name)
		}
	}
	return counts
}

// throttleCount sums the sub-buckets still inside the window.
func throttleCount(counts map[string]int, window ThrottleWindow, nowMs int64) int {
	width := slotWidth(window)
	if width <= 0 {
		return 0
	}
	current := nowMs / width.Milliseconds()
	oldest := current - int64(window.Slots) + 1
	total := 0
	for name, value := range counts {
		index, err := strconv.ParseInt(name, 10, 64)
		if err != nil || index < oldest || index > current {
			continue
		}
		total += value
	}
	return total
}

// throttleKeep is how long the row must live: a live block always outlives the
// retention, so the block is not forgotten early. A non-positive retention is
// floored so the row still expires.
func throttleKeep(retention, block time.Duration) time.Duration {
	ttl := retention
	if block > ttl {
		ttl = block
	}
	if ttl <= 0 {
		ttl = time.Second
	}
	return ttl
}
