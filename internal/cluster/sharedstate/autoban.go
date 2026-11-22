package sharedstate

import (
	"context"
	"database/sql"
	"time"
)

type AutoBanSpec struct {
	Window    time.Duration
	Slots     int
	Threshold int
	ClaimTTL  time.Duration
	Retention time.Duration
}

type AutoBanCharge struct {
	Failures      int
	Bans          int
	AlreadyBanned bool // A ban or another node's claim is in force.
	Claimed       bool // This node must write the deny rule.
}

type autoBanValue struct {
	Failures    timeBuckets `json:"failures,omitempty"`
	Bans        int         `json:"bans"`
	BannedUntil time.Time   `json:"banned_until"`
	ClaimUntil  time.Time   `json:"claim_until"`
}

func autoBanKey(cidr string) string { return "autoban:" + cidr }

// AutoBanCharge counts one failure and grants at most one active claim per source.
func (s *Store) AutoBanCharge(ctx context.Context, cidr string, spec AutoBanSpec, _ time.Time) (AutoBanCharge, error) {
	key := autoBanKey(cidr)
	var out AutoBanCharge
	err := s.transaction(ctx, key, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
		row, err := loadKV[autoBanValue](ctx, tx, key, now)
		if err != nil {
			return err
		}
		v := &row.Value
		out.Bans = v.Bans
		out.AlreadyBanned = v.BannedUntil.After(now) || v.ClaimUntil.After(now)
		if !out.AlreadyBanned {
			out.Failures = v.Failures.count(now, ThrottleWindow{max(1, spec.Threshold), spec.Window, spec.Slots}, true)
			if out.Failures >= spec.Threshold {
				v.ClaimUntil = now.Add(spec.ClaimTTL)
				out.Claimed = true
			}
		}
		row.ExpiresAt = now.Add(max(spec.Retention, v.BannedUntil.Sub(now), v.ClaimUntil.Sub(now)))
		return saveKV(ctx, tx, key, row)
	})
	return out, err
}

// AutoBanMark records the applied ban, resets the window, and releases the claim.
func (s *Store) AutoBanMark(ctx context.Context, cidr string, until, _ time.Time, retention time.Duration) error {
	key := autoBanKey(cidr)
	return s.transaction(ctx, key, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
		row, err := loadKV[autoBanValue](ctx, tx, key, now)
		if err != nil {
			return err
		}
		row.Value = autoBanValue{Bans: row.Value.Bans + 1, BannedUntil: until}
		row.ExpiresAt = now.Add(max(retention, until.Sub(now)))
		return saveKV(ctx, tx, key, row)
	})
}
