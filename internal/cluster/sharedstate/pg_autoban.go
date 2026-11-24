package sharedstate

import (
	"context"
	"fmt"
	"strconv"
	"time"

	pgredis "github.com/zuohuadong/pgredis-go"
)

// Auto-ban counters in PostgreSQL.
//
// Like the throttle, the decision is taken in Go on a snapshot and written back
// with compare-and-swap. The claim is what makes it correct across nodes: crossing
// the threshold also takes a short lease on the source, so exactly one node
// writes the deny rule, and the others see the source as already being handled.

// autoBanRetries bounds the compare-and-swap loop for one charge.
const autoBanRetries = 24

// autoBanState is the JSON payload of one source; the short field names keep
// the stored value small.
type autoBanState struct {
	Failures     map[string]int `json:"s,omitempty"`
	Bans         int            `json:"bc,omitempty"`
	BannedUntil  int64          `json:"bu,omitempty"`
	ClaimedUntil int64          `json:"cl,omitempty"`
	LastActivity int64          `json:"la,omitempty"`
}

// pgAutoBanKey names the source's row. It is kept readable: a network prefix is
// not a secret and an operator clearing one source's history by hand needs to
// find it.
func pgAutoBanKey(cidr string) string {
	return "autoban:" + cidr
}

// AutoBanCharge charges one authentication failure against cidr.
func (s *PGStore) AutoBanCharge(ctx context.Context, cidr string, spec AutoBanSpec, now time.Time) (AutoBanCharge, error) {
	if !s.Available() {
		return AutoBanCharge{}, ErrUnavailable
	}
	key := pgAutoBanKey(cidr)
	for attempt := 0; attempt < autoBanRetries; attempt++ {
		var state autoBanState
		found, err := s.store.Get(ctx, key, &state)
		if err != nil {
			return AutoBanCharge{}, err
		}
		next, charge, ttl := autoBanChargeAdvance(state.clone(), spec, now)
		var swapped bool
		if found {
			swapped, err = s.store.CompareAndSwapEqual(ctx, key, state, next, pgredis.TTL(ttl))
		} else {
			swapped, err = s.store.Put(ctx, key, next, pgredis.KVPutOptions{TTL: pgredis.TTL(ttl), NX: true})
		}
		if err != nil {
			return AutoBanCharge{}, err
		}
		if swapped {
			return charge, nil
		}
	}
	return AutoBanCharge{}, fmt.Errorf("sharedstate: auto-ban source %q is too contended to update", cidr)
}

// AutoBanMark records that cidr is banned until until.
func (s *PGStore) AutoBanMark(ctx context.Context, cidr string, until, now time.Time, retention time.Duration) error {
	if !s.Available() {
		return ErrUnavailable
	}
	key := pgAutoBanKey(cidr)
	for attempt := 0; attempt < autoBanRetries; attempt++ {
		var state autoBanState
		found, err := s.store.Get(ctx, key, &state)
		if err != nil {
			return err
		}
		next, ttl := autoBanMarkAdvance(state.clone(), until, now, retention)
		var swapped bool
		if found {
			swapped, err = s.store.CompareAndSwapEqual(ctx, key, state, next, pgredis.TTL(ttl))
		} else {
			swapped, err = s.store.Put(ctx, key, next, pgredis.KVPutOptions{TTL: pgredis.TTL(ttl), NX: true})
		}
		if err != nil {
			return err
		}
		if swapped {
			return nil
		}
	}
	return fmt.Errorf("sharedstate: auto-ban source %q is too contended to update", cidr)
}

// clone copies the state so an update can mutate its map in place without
// disturbing the value that was read.
func (s autoBanState) clone() autoBanState {
	out := s
	if s.Failures != nil {
		out.Failures = make(map[string]int, len(s.Failures))
		for k, v := range s.Failures {
			out.Failures[k] = v
		}
	}
	return out
}

// autoBanChargeAdvance applies one failure. It mirrors autoBanChargeScript.
func autoBanChargeAdvance(state autoBanState, spec AutoBanSpec, now time.Time) (autoBanState, AutoBanCharge, time.Duration) {
	nowMs := now.UnixMilli()
	retention := spec.Retention

	// A source already banned, or being acted on by another node, is left
	// alone: failures during that time must not extend the ban.
	if horizon := state.BannedUntil; horizon > nowMs || state.ClaimedUntil > nowMs {
		if state.ClaimedUntil > horizon {
			horizon = state.ClaimedUntil
		}
		return state, AutoBanCharge{Bans: state.Bans, AlreadyBanned: true},
			autoBanKeep(retention, time.Duration(horizon-nowMs)*time.Millisecond)
	}

	state.LastActivity = nowMs
	widthMs := int64(1)
	if spec.Slots > 0 && spec.Window/time.Duration(spec.Slots) >= time.Millisecond {
		widthMs = (spec.Window / time.Duration(spec.Slots)).Milliseconds()
	}
	current := nowMs / widthMs
	if state.Failures == nil {
		state.Failures = make(map[string]int, spec.Slots)
	}
	state.Failures[strconv.FormatInt(current, 10)]++
	oldest := current - int64(spec.Slots) + 1
	total := 0
	for name, value := range state.Failures {
		index, err := strconv.ParseInt(name, 10, 64)
		if err != nil {
			continue
		}
		switch {
		case index < oldest:
			delete(state.Failures, name)
		case index <= current:
			total += value
		}
	}

	charge := AutoBanCharge{Failures: total, Bans: state.Bans}
	if total >= spec.Threshold {
		// Crossing the threshold takes a short claim, so exactly one node
		// writes the rule; the claim lapses if that node fails.
		state.ClaimedUntil = nowMs + spec.ClaimTTL.Milliseconds()
		charge.Claimed = true
	}
	return state, charge, autoBanKeep(retention, 0)
}

// autoBanMarkAdvance records a ban: it counts it for escalation, clears the
// window so the same failures cannot re-trigger once the claim lapses, and
// releases the claim. It mirrors autoBanMarkScript.
func autoBanMarkAdvance(state autoBanState, until, now time.Time, retention time.Duration) (autoBanState, time.Duration) {
	state.Failures = nil
	state.ClaimedUntil = 0
	state.Bans++
	state.BannedUntil = until.UnixMilli()
	state.LastActivity = now.UnixMilli()
	return state, autoBanKeep(retention, until.Sub(now))
}

// autoBanKeep is how long the row must live: a ban always outlives the
// retention, so the escalation history is not lost while it is in force.
func autoBanKeep(retention, ban time.Duration) time.Duration {
	ttl := retention
	if ban > ttl {
		ttl = ban
	}
	if ttl <= 0 {
		ttl = time.Second
	}
	return ttl
}
