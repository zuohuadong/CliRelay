package clusterruntime

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	pgredis "github.com/zuohuadong/pgredis-go"
)

// pgSlotAdapter counts per-account concurrency slots in the cluster database.
//
// The pool is a set of members scored by expiry, and the "slot left" decision
// is one statement (see pgredis.TryReserveSlot). It is installed whenever
// cluster mode is on and the runtime database is available.
type pgSlotAdapter struct {
	store  *pgredis.Store
	coord  func() *cluster.Coordinator
	nodeID string
	keeper *slotKeeper

	mu     sync.Mutex
	held   map[string][]string // authID -> members, in acquisition order
	closed atomic.Bool
}

func newPGSlotAdapter(store *pgredis.Store, coord func() *cluster.Coordinator, nodeID string) *pgSlotAdapter {
	return &pgSlotAdapter{
		store:  store,
		coord:  coord,
		nodeID: nodeID,
		keeper: newSlotKeeper(store),
		held:   make(map[string][]string),
	}
}

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
	reservation, err := a.store.TryReserveSlot(ctx, ref.key, ref.member, limit, a.keeper.retention)
	if err != nil {
		return false, err
	}
	if !reservation.Taken {
		return false, nil
	}
	a.mu.Lock()
	a.held[authID] = append(a.held[authID], ref.member)
	a.mu.Unlock()
	a.keeper.Track(ref)
	return true, nil
}

// Release gives back one slot, the one this process acquired last for authID.
func (a *pgSlotAdapter) Release(authID string) {
	if a == nil || a.closed.Load() {
		return
	}
	a.mu.Lock()
	members := a.held[authID]
	member := ""
	if len(members) > 0 {
		member = members[len(members)-1]
		if len(members) == 1 {
			delete(a.held, authID)
		} else {
			a.held[authID] = members[:len(members)-1]
		}
	}
	a.mu.Unlock()
	if member == "" {
		return
	}
	a.keeper.Release(slotRef{key: slotKey(authID), member: member})
}

// Close stops the keeper. Remaining slots are left to expire.
func (a *pgSlotAdapter) Close() {
	if a == nil || a.closed.Swap(true) {
		return
	}
	a.keeper.Close()
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
