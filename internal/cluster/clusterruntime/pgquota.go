package clusterruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster/sharedstate"
	pgredis "github.com/zuohuadong/pgredis-go"
)

// Per-subject quota counters in the cluster database.
//
// Every node sees only part of a key's traffic, so per-node windows admit N
// times the limit. This adapter keeps one counter per wall-clock minute for
// requests and one for tokens in the cluster database, and takes the
// trailing-minute value as the current bucket plus the previous one weighted by
// the part of it still inside the last minute, so the arithmetic the middleware
// applies is unchanged.
//
// Counting and slot-taking are no longer one round trip, and that is deliberate:
// the window counters only feed a decision the caller re-checks against the
// usage database anyway, while the scarce resource, a concurrency slot, keeps
// its single-statement guarantee through the pool.

const (
	// pgQuotaBucketTTL keeps a minute bucket while it can still be the previous
	// minute.
	pgQuotaBucketTTL = 3 * time.Minute
	// pgQuotaFlush is how often asynchronously counted requests and tokens are
	// written. Tokens are known only after a request completes, so a delay of a
	// second is immaterial next to a one-minute window.
	pgQuotaFlush = time.Second
	// pgQuotaFlushTimeout bounds one flush, so a slow database does not let the
	// batch grow without bound.
	pgQuotaFlushTimeout = 5 * time.Second
)

// pgQuotaAdapter implements middleware.ClusterQuota on the cluster database.
type pgQuotaAdapter struct {
	store  *pgredis.Store
	coord  func() *cluster.Coordinator
	nodeID string
	keeper *slotKeeper
	now    func() time.Time

	mu      sync.Mutex
	pending map[pgQuotaCount]pgQuotaDelta
	stop    chan struct{}
	closed  atomic.Bool
	wg      sync.WaitGroup
}

type pgQuotaCount struct {
	subject string
	minute  int64
}

type pgQuotaDelta struct {
	requests int64
	tokens   int64
}

func newPGQuotaAdapter(store *pgredis.Store, coord func() *cluster.Coordinator, nodeID string) *pgQuotaAdapter {
	a := &pgQuotaAdapter{
		store:   store,
		coord:   coord,
		nodeID:  nodeID,
		keeper:  newSlotKeeper(store),
		now:     time.Now,
		pending: make(map[pgQuotaCount]pgQuotaDelta),
		stop:    make(chan struct{}),
	}
	a.wg.Add(1)
	go a.runFlush()
	return a
}

// Available reports whether the shared counters can be used right now.
func (a *pgQuotaAdapter) Available() bool {
	return a != nil && !a.closed.Load() && a.store != nil
}

// ActiveNodes is the number of live nodes, at least 1.
func (a *pgQuotaAdapter) ActiveNodes() int {
	if a == nil || a.coord == nil {
		return 1
	}
	if c := a.coord(); c != nil {
		if n := c.ActiveNodeCount(); n > 1 {
			return n
		}
	}
	return 1
}

// AdmitRequest counts one request and, when concurrencyLimit is positive, tries
// to take a concurrency slot.
func (a *pgQuotaAdapter) AdmitRequest(ctx context.Context, subject string, concurrencyLimit int) (sharedstate.Admission, error) {
	if !a.Available() {
		return sharedstate.Admission{}, sharedstate.ErrUnavailable
	}
	if concurrencyLimit < 0 {
		concurrencyLimit = 0
	}
	now := a.now()
	minute, elapsed := pgQuotaMinute(now)
	weight := float64(int64(time.Minute/time.Millisecond)-elapsed) / float64(int64(time.Minute/time.Millisecond))

	rpmCur, err := a.store.CounterAdd(ctx, pgQuotaKey(subject, "rpm", minute), 1, pgredis.TTL(pgQuotaBucketTTL))
	if err != nil {
		return sharedstate.Admission{}, err
	}
	rpmPrev, err := a.counter(ctx, pgQuotaKey(subject, "rpm", minute-1))
	if err != nil {
		return sharedstate.Admission{}, err
	}
	tpmCur, err := a.counter(ctx, pgQuotaKey(subject, "tpm", minute))
	if err != nil {
		return sharedstate.Admission{}, err
	}
	tpmPrev, err := a.counter(ctx, pgQuotaKey(subject, "tpm", minute-1))
	if err != nil {
		return sharedstate.Admission{}, err
	}

	adm := sharedstate.Admission{
		RPM:       float64(rpmPrev)*weight + float64(rpmCur),
		TPM:       float64(tpmPrev)*weight + float64(tpmCur),
		Release:   func() {},
		SlotTaken: false,
	}
	if concurrencyLimit <= 0 {
		return adm, nil
	}
	ref := slotRef{key: pgQuotaSlotKey(subject), member: a.memberName()}
	reservation, err := a.store.TryReserveSlot(ctx, ref.key, ref.member, concurrencyLimit, a.keeper.retention)
	if err != nil {
		return adm, err
	}
	adm.Held = reservation.Held
	if !reservation.Taken {
		return adm, nil
	}
	a.keeper.Track(ref)
	var once sync.Once
	adm.SlotTaken = true
	adm.Release = func() {
		once.Do(func() { a.keeper.Release(ref) })
	}
	return adm, nil
}

// CountRequest counts a request without waiting, for subjects with no rate or
// concurrency limit.
func (a *pgQuotaAdapter) CountRequest(subject string) {
	if !a.Available() || subject == "" {
		return
	}
	a.accumulate(subject, 1, 0)
}

// AddTokens adds token usage without waiting.
func (a *pgQuotaAdapter) AddTokens(subject string, tokens int64) {
	if !a.Available() || subject == "" || tokens <= 0 {
		return
	}
	a.accumulate(subject, 0, tokens)
}

// Rates reads the trailing-minute windows of subjects, for the dashboard.
func (a *pgQuotaAdapter) Rates(ctx context.Context, subjects []string) (map[string]sharedstate.Rate, error) {
	if !a.Available() {
		return nil, sharedstate.ErrUnavailable
	}
	minute, elapsed := pgQuotaMinute(a.now())
	weight := float64(int64(time.Minute/time.Millisecond)-elapsed) / float64(int64(time.Minute/time.Millisecond))
	out := make(map[string]sharedstate.Rate, len(subjects))
	for _, subject := range subjects {
		rpmCur, err := a.counter(ctx, pgQuotaKey(subject, "rpm", minute))
		if err != nil {
			return nil, err
		}
		rpmPrev, err := a.counter(ctx, pgQuotaKey(subject, "rpm", minute-1))
		if err != nil {
			return nil, err
		}
		tpmCur, err := a.counter(ctx, pgQuotaKey(subject, "tpm", minute))
		if err != nil {
			return nil, err
		}
		tpmPrev, err := a.counter(ctx, pgQuotaKey(subject, "tpm", minute-1))
		if err != nil {
			return nil, err
		}
		out[subject] = sharedstate.Rate{
			RPM: float64(rpmPrev)*weight + float64(rpmCur),
			TPM: float64(tpmPrev)*weight + float64(tpmCur),
		}
	}
	return out, nil
}

// Close stops the workers. Slots still held are left to expire.
func (a *pgQuotaAdapter) Close() {
	if a == nil || a.closed.Swap(true) {
		return
	}
	close(a.stop)
	a.wg.Wait()
	a.keeper.Close()
}

func (a *pgQuotaAdapter) counter(ctx context.Context, key string) (int64, error) {
	value, _, err := a.store.CounterGet(ctx, key)
	return value, err
}

func (a *pgQuotaAdapter) accumulate(subject string, requests, tokens int64) {
	minute, _ := pgQuotaMinute(a.now())
	a.mu.Lock()
	delta := a.pending[pgQuotaCount{subject: subject, minute: minute}]
	delta.requests += requests
	delta.tokens += tokens
	a.pending[pgQuotaCount{subject: subject, minute: minute}] = delta
	a.mu.Unlock()
}

func (a *pgQuotaAdapter) runFlush() {
	defer a.wg.Done()
	ticker := time.NewTicker(pgQuotaFlush)
	defer ticker.Stop()
	for {
		select {
		case <-a.stop:
			a.flush()
			return
		case <-ticker.C:
			a.flush()
		}
	}
}

func (a *pgQuotaAdapter) flush() {
	a.mu.Lock()
	batch := a.pending
	a.pending = make(map[pgQuotaCount]pgQuotaDelta)
	a.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pgQuotaFlushTimeout)
	defer cancel()
	for key, delta := range batch {
		if delta.requests > 0 {
			if _, err := a.store.CounterAdd(ctx, pgQuotaKey(key.subject, "rpm", key.minute), delta.requests, pgredis.TTL(pgQuotaBucketTTL)); err != nil {
				log.WithError(err).Debug("cluster: flushing counted requests to PostgreSQL failed")
			}
		}
		if delta.tokens > 0 {
			if _, err := a.store.CounterAdd(ctx, pgQuotaKey(key.subject, "tpm", key.minute), delta.tokens, pgredis.TTL(pgQuotaBucketTTL)); err != nil {
				log.WithError(err).Debug("cluster: flushing counted tokens to PostgreSQL failed")
			}
		}
	}
}

// memberName names one held slot. Operators read the set by prefix; uniqueness
// comes from the random suffix.
func (a *pgQuotaAdapter) memberName() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return a.nodeID + ":quota:" + hex.EncodeToString(buf[:])
}

// pgQuotaMinute splits a timestamp into its wall-clock minute and the
// milliseconds elapsed in it.
func pgQuotaMinute(now time.Time) (minute, elapsed int64) {
	ms := now.UnixMilli()
	return ms / int64(time.Minute/time.Millisecond), ms % int64(time.Minute/time.Millisecond)
}

// pgQuotaKey names one bucket. The subject is hashed: an API key or an end-user
// identity is a secret and must not land in a key an operator can browse.
func pgQuotaKey(subject, kind string, minute int64) string {
	return "rl:" + sharedstate.HashPart(subject) + ":" + kind + ":" + strconv.FormatInt(minute, 10)
}

// pgQuotaSlotKey is the per-subject concurrency pool. The limit applies to the
// subject's own pool only, which is why the key cannot be shared.
func pgQuotaSlotKey(subject string) string {
	return "quota-slots:" + sharedstate.HashPart(subject)
}
