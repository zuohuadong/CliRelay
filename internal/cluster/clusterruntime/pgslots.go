package clusterruntime

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/pgredis"
)

// pgSlotRetention is how long a slot survives without renewal. A process that
// dies holding slots leaks them for at most this long; the renewer below keeps
// live holders alive well inside it.
const pgSlotRetention = 5 * time.Minute

// pgSlotRenewInterval renews held slots. It is a third of the retention, so two
// consecutive renewals may fail before a holder loses its slots, which absorbs
// a blip without extending the leak past the retention window.
const pgSlotRenewInterval = pgSlotRetention / 3

// pgReleaseQueue bounds the asynchronous releases. The AccountSlotCoordinator
// contract calls Release under the limiter's lock, so the delete cannot happen
// inline; a bounded queue with a drop-and-expire fallback keeps that
// non-blocking without unbounded goroutines.
const pgReleaseQueue = 4096

// pgSlotAdapter counts per-account concurrency slots in the cluster database
// instead of the cluster Redis.
//
// It implements the same AccountSlotCoordinator contract as the Redis-backed
// store: the pool is a sorted set of members scored by expiry, and the "slot
// left" decision is one statement (see pgredis.TryReserveSlot). It is installed
// only when cluster mode is on, no cluster Redis is configured and the
// PostgreSQL pool is available, so a deployment that already has Redis keeps
// using it.
type pgSlotAdapter struct {
	store     *pgredis.Store
	coord     func() *cluster.Coordinator
	nodeID    string
	retention time.Duration

	mu     sync.Mutex
	held   map[string][]slotRef
	renew  map[string]slotRef
	queue  chan slotRelease
	stop   chan struct{}
	closed atomic.Bool
	wg     sync.WaitGroup
}

// slotRef identifies one held slot: the per-account pool key plus the member
// that holds it. Releases and renewals need both.
type slotRef struct {
	key    string
	member string
}

type slotRelease struct {
	ref slotRef
}

func newPGSlotAdapter(store *pgredis.Store, coord func() *cluster.Coordinator, nodeID string) *pgSlotAdapter {
	a := &pgSlotAdapter{
		store:     store,
		coord:     coord,
		nodeID:    nodeID,
		retention: pgSlotRetention,
		held:      make(map[string][]slotRef),
		renew:     make(map[string]slotRef),
		queue:     make(chan slotRelease, pgReleaseQueue),
		stop:      make(chan struct{}),
	}
	a.wg.Add(2)
	go a.runReleases()
	go a.runRenewals()
	return a
}

// Shared reports whether cluster-wide counting can be reached right now. The
// limiter falls back to NodeShare whenever this is false.
func (a *pgSlotAdapter) Shared() bool {
	return a != nil && !a.closed.Load() && a.store != nil
}

// NodeShare is the per-process share used while Shared is false.
func (a *pgSlotAdapter) NodeShare(limit int) int {
	nodes := 1
	if a != nil && a.coord != nil {
		if c := a.coord(); c != nil {
			nodes = c.ActiveNodeCount()
		}
	}
	return splitLimit(limit, nodes)
}

// Acquire takes one slot for authID when the pool has room.
func (a *pgSlotAdapter) Acquire(authID string, limit int) (bool, error) {
	if !a.Shared() {
		return false, pgredis.ErrUnavailable
	}
	ref := slotRef{key: slotKey(authID), member: a.memberName(authID)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reservation, err := a.store.TryReserveSlot(ctx, ref.key, ref.member, limit, a.retention)
	if err != nil {
		return false, err
	}
	if !reservation.Taken {
		return false, nil
	}
	a.mu.Lock()
	a.held[authID] = append(a.held[authID], ref)
	a.renew[ref.member] = ref
	a.mu.Unlock()
	return true, nil
}

// Release gives back one slot. It must not block, so the delete is handed to
// the release worker; a dropped release costs one slot until its retention
// expires.
func (a *pgSlotAdapter) Release(authID string) {
	if a == nil || a.closed.Load() {
		return
	}
	a.mu.Lock()
	members := a.held[authID]
	var ref slotRef
	if len(members) > 0 {
		ref = members[len(members)-1]
		a.held[authID] = members[:len(members)-1]
		if len(a.held[authID]) == 0 {
			delete(a.held, authID)
		}
	}
	if ref.member != "" {
		delete(a.renew, ref.member)
	}
	a.mu.Unlock()
	if ref.member == "" {
		return
	}
	select {
	case a.queue <- slotRelease{ref: ref}:
	default:
		// The queue is full: the slot stays until its score expires, which is
		// the same failure mode as a lost Redis DEL but bounded in time.
		log.Warn("cluster: slot release queue is full; releasing through expiry")
	}
}

// Close stops the workers. Remaining slots are left to expire.
func (a *pgSlotAdapter) Close() {
	if a == nil || a.closed.Swap(true) {
		return
	}
	close(a.stop)
	a.wg.Wait()
}

func (a *pgSlotAdapter) runReleases() {
	defer a.wg.Done()
	for {
		select {
		case <-a.stop:
			return
		case release := <-a.queue:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if _, err := a.store.ReleaseSlot(ctx, release.ref.key, release.ref.member); err != nil {
				log.WithError(err).Debug("cluster: releasing a slot through PostgreSQL failed; it will expire")
			}
			cancel()
		}
	}
}

// runRenewals extends every slot this process still holds, so a long request
// does not lose its slot while it runs.
func (a *pgSlotAdapter) runRenewals() {
	defer a.wg.Done()
	ticker := time.NewTicker(pgSlotRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-ticker.C:
			a.renewHeld()
		}
	}
}

func (a *pgSlotAdapter) renewHeld() {
	a.mu.Lock()
	refs := make([]slotRef, 0, len(a.renew))
	for _, ref := range a.renew {
		refs = append(refs, ref)
	}
	a.mu.Unlock()
	if len(refs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, ref := range refs {
		if _, err := a.store.RenewSlot(ctx, ref.key, ref.member, a.retention); err != nil {
			log.WithError(err).Debug("cluster: renewing a PostgreSQL slot failed")
		}
	}
}

func (a *pgSlotAdapter) memberName(authID string) string {
	return a.nodeID + "|" + authID + "|" + time.Now().Format("150405.000000000")
}

// slotKey is the per-account pool. The limit an account is allowed applies to
// its own pool only, which is why the key cannot be shared: TryReserveSlot
// counts the members of one key.
func slotKey(authID string) string {
	return "account-slots:" + authID
}

// pgSlotPool builds the PostgreSQL slot store once per process. It returns nil
// when the coordinator has no database, so callers keep the split-limit
// behaviour instead of failing.
func pgSlotPool(coord func() *cluster.Coordinator) *pgredis.Store {
	if coord == nil {
		return nil
	}
	c := coord()
	if c == nil {
		return nil
	}
	db := c.DB()
	if db == nil {
		return nil
	}
	store := pgredis.New(db, pgredis.Options{Namespace: "cluster", TablePrefix: "clirelay"})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.EnsureSchema(ctx); err != nil {
		log.WithError(err).Warn("cluster: cannot create the PostgreSQL slot tables; account slots stay per node")
		return nil
	}
	return store
}
