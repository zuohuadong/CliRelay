package sharedstate

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type MintedID struct {
	ID     string
	TTL    time.Duration // Local caches must not outlive the shared ID.
	Minted bool
}

// MintID returns the existing ID or stores the caller's candidate. Sliding
// IDs renew on read; fixed IDs keep their original expiry.
func (s *Store) MintID(ctx context.Context, namespace, key, candidate string, ttl time.Duration, sliding bool) (MintedID, error) {
	if candidate == "" || ttl <= 0 {
		return MintedID{}, errors.New("sharedstate: candidate and positive TTL required")
	}
	scope := stateKey("synthetic-id", namespace, key)
	var out MintedID
	err := s.transaction(ctx, scope, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
		row, err := loadKV[string](ctx, tx, scope, now)
		if err != nil {
			return err
		}
		out.Minted = !row.Found
		if out.Minted {
			row.Value = candidate
		}
		if out.Minted || sliding {
			row.ExpiresAt = now.Add(ttl)
			if err = saveKV(ctx, tx, scope, row); err != nil {
				return err
			}
		}
		out.ID, out.TTL = row.Value, min(ttl, row.ExpiresAt.Sub(now))
		return nil
	})
	return out, err
}
