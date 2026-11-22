package sharedstate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

func (s *Store) acquireLease(ctx context.Context, key, member string, limit int) (bool, int, error) {
	var taken bool
	var held int
	err := s.transaction(ctx, key, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
		var err error
		taken, held, err = s.takeSlot(ctx, tx, key, member, limit, now)
		return err
	})
	return taken && err == nil, held, err
}

// takeSlot runs under the scope advisory lock, including when no rows exist.
func (s *Store) takeSlot(ctx context.Context, tx *sql.Tx, scope, member string, limit int, now time.Time) (bool, int, error) {
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM cluster_runtime_leases
		WHERE scope = $1 AND expires_at > $2`, scope, now).Scan(&held); err != nil {
		return false, 0, err
	}
	if held >= limit {
		return false, held, nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO cluster_runtime_leases (scope, member, expires_at)
		VALUES ($1, $2, $3)`, scope, member, now.Add(s.leaseTTL))
	return err == nil, held + 1, err
}

// newLeaseMember names one held slot. The node prefix is for operators reading
// the set; uniqueness comes from the random suffix.
func (s *Store) newLeaseMember() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return s.nodeID + ":" + hex.EncodeToString(buf[:])
}

type leaseRef struct {
	Key    string `json:"scope"`
	Member string `json:"member"`
}

// leaseKeeper renews the leases this node holds and gives back released ones.
//
// Renewals and releases run on one goroutine, in order. Expired leases cannot
// be revived: another node may already have taken the newly available slot.
type leaseKeeper struct {
	store *Store

	mu      sync.Mutex
	held    map[leaseRef]struct{}
	pending map[leaseRef]time.Time

	releases  chan leaseRef
	stop      chan struct{}
	done      chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	started   bool
}

func newLeaseKeeper(s *Store) *leaseKeeper {
	return &leaseKeeper{
		store:    s,
		held:     make(map[leaseRef]struct{}),
		pending:  make(map[leaseRef]time.Time),
		releases: make(chan leaseRef, 4096),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (k *leaseKeeper) ensureStarted() {
	k.startOnce.Do(func() {
		k.mu.Lock()
		k.started = true
		k.mu.Unlock()
		go k.run()
	})
}

// track starts renewing a lease that was just taken.
func (k *leaseKeeper) track(key, member string) {
	k.ensureStarted()
	k.mu.Lock()
	k.held[leaseRef{key, member}] = struct{}{}
	k.mu.Unlock()
}

// release stops renewing a lease and removes it without blocking
// the caller. If PostgreSQL cannot be reached the removal is retried until the
// lease would have expired on its own.
func (k *leaseKeeper) release(key, member string) {
	ref := leaseRef{key, member}
	k.mu.Lock()
	delete(k.held, ref)
	k.mu.Unlock()
	k.ensureStarted()
	select {
	case k.releases <- ref:
	default:
		k.mu.Lock()
		k.pending[ref] = k.store.localTime().Add(k.store.leaseTTL)
		k.mu.Unlock()
	}
}

func (k *leaseKeeper) run() {
	defer close(k.done)
	ticker := time.NewTicker(k.store.leaseTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-k.stop:
			k.releaseAllOnClose()
			return
		case ref := <-k.releases:
			k.removeBatch(ref)
		case <-ticker.C:
			k.renew()
			k.retryPending()
		}
	}
}

// removeBatch removes ref and whatever else is already queued in one pipeline.
func (k *leaseKeeper) removeBatch(first leaseRef) {
	batch := []leaseRef{first}
	for len(batch) < 256 {
		select {
		case ref := <-k.releases:
			batch = append(batch, ref)
			continue
		default:
		}
		break
	}
	if err := k.remove(batch); err != nil {
		deadline := k.store.localTime().Add(k.store.leaseTTL)
		k.mu.Lock()
		for _, ref := range batch {
			k.pending[ref] = deadline
		}
		k.mu.Unlock()
	}
}

func (k *leaseKeeper) remove(batch []leaseRef) error {
	if !k.store.Available() {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), backgroundTimeout)
	defer cancel()
	data, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	_, err = k.store.db.ExecContext(ctx, `DELETE FROM cluster_runtime_leases l
		USING jsonb_to_recordset($1) AS r(scope text, member text)
		WHERE l.scope = r.scope AND l.member = r.member`, data)
	return err
}

func (k *leaseKeeper) renew() {
	k.mu.Lock()
	refs := make([]leaseRef, 0, len(k.held))
	for ref := range k.held {
		refs = append(refs, ref)
	}
	k.mu.Unlock()
	if len(refs) == 0 || !k.store.Available() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), backgroundTimeout)
	defer cancel()
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key < refs[j].Key })
	for len(refs) != 0 && ctx.Err() == nil {
		n := min(128, len(refs))
		batch := refs[:n]
		refs = refs[n:]
		scopes := []string{batch[0].Key}
		for _, ref := range batch[1:] {
			if ref.Key != scopes[len(scopes)-1] {
				scopes = append(scopes, ref.Key)
			}
		}
		data, err := json.Marshal(batch)
		if err != nil {
			return
		}
		_ = k.store.transactionScopes(ctx, scopes, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
			_, err := tx.ExecContext(ctx, `UPDATE cluster_runtime_leases l SET expires_at = $3
				FROM jsonb_to_recordset($1) AS r(scope text, member text)
				WHERE l.scope = r.scope AND l.member = r.member AND l.expires_at > $2`,
				data, now, now.Add(k.store.leaseTTL))
			return err
		})
	}
}

func (k *leaseKeeper) retryPending() {
	now := k.store.localTime()
	k.mu.Lock()
	var batch []leaseRef
	for ref, deadline := range k.pending {
		if !deadline.After(now) {
			delete(k.pending, ref) // expired on its own by now
			continue
		}
		batch = append(batch, ref)
	}
	k.mu.Unlock()
	if len(batch) == 0 || !k.store.Available() {
		return
	}
	if err := k.remove(batch); err == nil {
		k.mu.Lock()
		for _, ref := range batch {
			delete(k.pending, ref)
		}
		k.mu.Unlock()
	}
}

func (k *leaseKeeper) releaseAllOnClose() {
	k.mu.Lock()
	batch := make([]leaseRef, 0, len(k.held)+len(k.pending))
	for ref := range k.held {
		batch = append(batch, ref)
	}
	for ref := range k.pending {
		batch = append(batch, ref)
	}
	k.held = make(map[leaseRef]struct{})
	k.pending = make(map[leaseRef]time.Time)
	k.mu.Unlock()
	for {
		select {
		case ref := <-k.releases:
			batch = append(batch, ref)
			continue
		default:
		}
		break
	}
	if len(batch) > 0 && k.store.Available() {
		_ = k.remove(batch)
	}
}

func (k *leaseKeeper) close() {
	k.closeOnce.Do(func() {
		close(k.stop)
		k.mu.Lock()
		started := k.started
		k.mu.Unlock()
		if started {
			<-k.done
		}
	})
}

// heldCount reports how many leases this node is renewing. Test-facing.
func (k *leaseKeeper) heldCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.held)
}
