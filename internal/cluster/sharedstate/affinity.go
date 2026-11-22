package sharedstate

import (
	"context"
	"database/sql"
	"time"
)

// Binding pins a session to an opaque upstream account reference.
type Binding struct {
	Account string
	Served  int // Requests served before this one.
	Found   bool
	Created bool
}

type affinityValue struct {
	Account string `json:"account"`
	Served  int    `json:"served"`
}

func affinityKey(sessionKey string) string { return stateKey("affinity", sessionKey) }

// AffinityLookup returns the current binding, counting this turn and renewing its TTL.
func (s *Store) AffinityLookup(ctx context.Context, sessionKey string, ttl time.Duration) (Binding, error) {
	return s.affinity(ctx, sessionKey, "", ttl, false)
}

// AffinityBind creates the binding if absent. Otherwise it uses the current winner.
func (s *Store) AffinityBind(ctx context.Context, sessionKey, account string, ttl time.Duration) (Binding, error) {
	return s.affinity(ctx, sessionKey, account, ttl, true)
}

func (s *Store) affinity(ctx context.Context, sessionKey, account string, ttl time.Duration, bind bool) (Binding, error) {
	key := affinityKey(sessionKey)
	var out Binding
	err := s.transaction(ctx, key, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
		row, err := loadKV[affinityValue](ctx, tx, key, now)
		if err != nil || (!row.Found && !bind) {
			return err
		}
		if !row.Found {
			row.Value.Account = account
		}
		out = Binding{Account: row.Value.Account, Served: row.Value.Served, Found: true, Created: !row.Found}
		row.Value.Served++
		row.ExpiresAt = now.Add(ttl)
		return saveKV(ctx, tx, key, row)
	})
	return out, err
}

// AffinityRelease deletes the binding only if it still names account.
func (s *Store) AffinityRelease(ctx context.Context, sessionKey, account string) error {
	key := affinityKey(sessionKey)
	return s.transaction(ctx, key, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
		row, err := loadKV[affinityValue](ctx, tx, key, now)
		if err != nil || !row.Found || row.Value.Account != account {
			return err
		}
		return deleteKV(ctx, tx, key)
	})
}
