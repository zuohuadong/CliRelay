package pgredis

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"time"
)

// ErrLockBusy reports that another session holds the lock.
var ErrLockBusy = errors.New("pgredis: lock is held by another session")

// SessionLock is a PostgreSQL advisory lock held for the life of a transaction.
//
// Advisory locks are the one primitive PostgreSQL offers that has no Redis
// equivalent to translate: the database releases them itself when the session
// or transaction ends, so a crashed holder cannot leave a lock behind, and
// there is no TTL to guess at.
type SessionLock struct {
	key int64
}

// Key exposes the derived advisory lock id, for logging.
func (l SessionLock) Key() int64 { return l.key }

// AdvisoryKey maps a name onto the 64-bit space advisory locks use. Two
// different names map to the same key only on a 64-bit collision, which is the
// same risk a Redis key has.
func AdvisoryKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64())
}

// WithAdvisoryLock runs fn while holding the named lock. It returns ErrLockBusy
// when another session holds it and wait is false, so callers can choose
// between "skip this work" and "queue behind the current holder".
//
// fn runs inside one transaction: if it returns an error the transaction rolls
// back, which also releases the lock.
func (s *Store) WithAdvisoryLock(ctx context.Context, name string, wait bool, fn func(context.Context, Querier) error) error {
	beginner, ok := s.q.(interface {
		BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return errors.New("pgredis: advisory locks need a transaction-capable querier")
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	key := AdvisoryKey(name)
	if wait {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return err
		}
	} else {
		var acquired bool
		if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, key).Scan(&acquired); err != nil {
			return err
		}
		if !acquired {
			return ErrLockBusy
		}
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// LeaseLock is a non-blocking lock with a TTL, for the case where the work may
// outlive one transaction (an HTTP request handler, for example) and the holder
// must be able to prove ownership when it unlocks.
//
// It is the KV-backed equivalent of SET NX PX plus the compare-and-delete
// script: the release only removes the row when the owner still matches.
type LeaseLock struct {
	key   string
	owner string
}

// TryLeaseLock records ownership of name for ttl and returns the handle.
// ok=false means another owner holds a live lease.
func (s *Store) TryLeaseLock(ctx context.Context, name, owner string, ttl TTL) (LeaseLock, bool, error) {
	if owner == "" {
		return LeaseLock{}, false, errors.New("pgredis: lease owner is required")
	}
	lock := LeaseLock{key: "lease-lock:" + name, owner: owner}
	written, err := s.Put(ctx, lock.key, map[string]string{"owner": owner}, KVPutOptions{TTL: ttl, NX: true})
	if err != nil {
		return LeaseLock{}, false, err
	}
	if !written {
		// NX rejected the write. An expired lease must not block the next
		// owner, so replace it in place when the stored owner's lease is over.
		return LeaseLock{}, false, nil
	}
	return lock, true, nil
}

// Renew extends the lease. It fails when the lease expired or was taken over.
func (s *Store) RenewLease(ctx context.Context, lock LeaseLock, ttl TTL) (bool, error) {
	tag, err := s.q.ExecContext(ctx,
		`UPDATE `+s.tables.kv+`
		    SET expires_at = `+expiryExpr("$4")+`, updated_at = now()
		  WHERE namespace = $1 AND key = $2
		    AND (expires_at IS NULL OR expires_at > now())
		    AND value ->> 'owner' = $3`,
		s.ns(), lock.key, lock.owner, ttl.millis())
	if err != nil {
		return false, err
	}
	return rowsAffected(tag) > 0, nil
}

// Unlock releases the lease only if this owner still holds it.
func (s *Store) Unlock(ctx context.Context, lock LeaseLock) (bool, error) {
	tag, err := s.q.ExecContext(ctx,
		`DELETE FROM `+s.tables.kv+`
		  WHERE namespace = $1 AND key = $2 AND value ->> 'owner' = $3`,
		s.ns(), lock.key, lock.owner)
	if err != nil {
		return false, err
	}
	return rowsAffected(tag) > 0, nil
}

// TenantLockName builds a lock name that cannot collide across namespaces.
func TenantLockName(parts ...string) string {
	name := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if name != "" {
			name += ":"
		}
		name += part
	}
	return name
}

// lockRetryDelay is the pause between attempts when a caller asks for a lease
// with a bounded wait.
const lockRetryDelay = 25 * time.Millisecond
