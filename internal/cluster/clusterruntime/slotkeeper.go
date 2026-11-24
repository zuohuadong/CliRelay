package clusterruntime

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	pgredis "github.com/zuohuadong/pgredis-go"
)

// Retaining heap slots in the cluster database.
//
// A slot is a member of a sorted set scored by its expiry, so holding one means
// renewing it before that score passes: this keeper renews everything it holds
// every few seconds, and a process that dies leaks its slots for at most the
// retention. Releases are queued because the callers run under a limiter's
// lock and must not block on the database; a dropped release is a slot that
// survives until its score expires, which is the same bounded failure mode as
// a lost DEL.
const (
	slotRetention     = 5 * time.Minute
	slotRenewInterval = slotRetention / 3
	slotReleaseQueue  = 4096
)

// slotRef identifies one held slot: the pool key plus the member holding it.
type slotRef struct {
	key    string
	member string
}

type slotRelease struct{ ref slotRef }

// slotKeeper renews and releases the slots one process holds, across all pools
// it uses (per-account concurrency and per-subject quota).
type slotKeeper struct {
	store     *pgredis.Store
	retention time.Duration
	interval  time.Duration

	mu     sync.Mutex
	held   map[slotRef]struct{}
	queue  chan slotRelease
	stop   chan struct{}
	closed atomic.Bool
	wg     sync.WaitGroup
}

func newSlotKeeper(store *pgredis.Store) *slotKeeper {
	k := &slotKeeper{
		store:     store,
		retention: slotRetention,
		interval:  slotRenewInterval,
		held:      make(map[slotRef]struct{}),
		queue:     make(chan slotRelease, slotReleaseQueue),
		stop:      make(chan struct{}),
	}
	k.wg.Add(2)
	go k.runReleases()
	go k.runRenewals()
	return k
}

// Track starts keeping a slot this process has just reserved.
func (k *slotKeeper) Track(ref slotRef) {
	if k == nil || ref.member == "" {
		return
	}
	k.mu.Lock()
	k.held[ref] = struct{}{}
	k.mu.Unlock()
}

// Release hands a slot back without blocking. Releasing a slot this keeper does
// not track is a no-op.
func (k *slotKeeper) Release(ref slotRef) {
	if k == nil || k.closed.Load() {
		return
	}
	k.mu.Lock()
	_, tracked := k.held[ref]
	delete(k.held, ref)
	k.mu.Unlock()
	if !tracked {
		return
	}
	select {
	case k.queue <- slotRelease{ref: ref}:
	default:
		log.Warn("cluster: slot release queue is full; releasing through expiry")
	}
}

// Close stops the workers. Slots still held are left to expire.
func (k *slotKeeper) Close() {
	if k == nil || k.closed.Swap(true) {
		return
	}
	close(k.stop)
	k.wg.Wait()
}

func (k *slotKeeper) runReleases() {
	defer k.wg.Done()
	for {
		select {
		case <-k.stop:
			return
		case release := <-k.queue:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if _, err := k.store.ReleaseSlot(ctx, release.ref.key, release.ref.member); err != nil {
				log.WithError(err).Debug("cluster: releasing a slot through PostgreSQL failed; it will expire")
			}
			cancel()
		}
	}
}

func (k *slotKeeper) runRenewals() {
	defer k.wg.Done()
	ticker := time.NewTicker(k.interval)
	defer ticker.Stop()
	for {
		select {
		case <-k.stop:
			return
		case <-ticker.C:
			k.renewHeld()
		}
	}
}

func (k *slotKeeper) renewHeld() {
	k.mu.Lock()
	refs := make([]slotRef, 0, len(k.held))
	for ref := range k.held {
		refs = append(refs, ref)
	}
	k.mu.Unlock()
	if len(refs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, ref := range refs {
		if _, err := k.store.RenewSlot(ctx, ref.key, ref.member, k.retention); err != nil {
			log.WithError(err).Debug("cluster: renewing a PostgreSQL slot failed")
		}
	}
}
