// Package clusterruntime wires the cluster-wide limits into a running node:
// it uses the runtime PostgreSQL pool and installs the shared counters,
// leases, bindings and minting into the quota middleware, the login throttle,
// the auto-ban engine, the executors and the auth manager, and relays upstream
// cooldowns between nodes.
//
// Nothing here runs outside cluster mode: a single node keeps every
// pre-cluster code path.
package clusterruntime

import (
	"context"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cluster"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cluster/sharedstate"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/ipaccess"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Runtime is one node's cluster wiring.
type Runtime struct {
	coord func() *cluster.Coordinator
	store *sharedstate.Store
	relay *cooldownRelay

	mu       sync.Mutex
	managers map[*coreauth.Manager]struct{}

	// refs counts the Install calls not yet released; guarded by installMu.
	refs int
}

var (
	installMu sync.Mutex
	installed *Runtime
)

// Install wires the cluster into this process for cfg, attaches manager and
// returns the process's runtime, or nil outside cluster mode. The runtime is
// shared: a later Install only attaches another manager. Every non-nil result
// must be handed back to Release.
//
// It must run after cluster.Prepare, which StartService calls first: the
// cooldown subscription has to be made on the coordinator that cluster.Start
// then adopts. The API server calls it while it is being built, before it
// listens.
func Install(cfg *config.Config, manager *coreauth.Manager) *Runtime {
	if cfg == nil || !cfg.Cluster.Enabled {
		return nil
	}
	installMu.Lock()
	defer installMu.Unlock()
	if installed == nil {
		installed = start(cluster.Default)
	}
	installed.refs++
	installed.Attach(manager)
	return installed
}

// Release hands back one Install. The last release gives this node's
// concurrency slots back to the cluster and flushes pending counters;
// call it once the server has drained. nil is a
// no-op.
func Release(rt *Runtime) {
	if rt == nil {
		return
	}
	installMu.Lock()
	rt.refs--
	last := rt.refs <= 0
	if last && installed == rt {
		installed = nil
	}
	installMu.Unlock()
	if last {
		rt.Close()
	}
}

// start builds the runtime. coord resolves the coordinator on every use.
func start(coord func() *cluster.Coordinator) *Runtime {
	c := coord()
	if !c.Enabled() {
		log.Warn("cluster: cluster mode is on but no cluster coordinator is running; cooldowns will not be shared")
	}
	nodeID := c.NodeID()

	store := sharedstate.New(usage.RuntimeDB(), sharedstate.Options{NodeID: nodeID})
	if !store.Available() {
		log.Warn("cluster: no runtime database; shared limits and affinity are unavailable")
	}
	return newRuntime(coord, store)
}

func newRuntime(coord func() *cluster.Coordinator, store *sharedstate.Store) *Runtime {
	rt := &Runtime{
		coord:    coord,
		store:    store,
		relay:    newCooldownRelay(coord),
		managers: make(map[*coreauth.Manager]struct{}),
	}
	rt.relay.start(coord())

	// Split limits need no database, so the quota middleware and the account
	// limiter are cluster-aware even without one. The guards and the id
	// minting only change behaviour when there is a store to share.
	middleware.SetClusterQuota(quotaAdapter{Store: store, coord: coord})
	if store.Available() {
		management.SetClusterThrottleStore(store)
		ipaccess.SetClusterAutoBanStore(store)
		executor.SetSharedIDMinter(store)
	}
	return rt
}

// Attach wires manager: cluster-wide account slots, shared session affinity
// and cooldown relaying.
func (rt *Runtime) Attach(manager *coreauth.Manager) {
	if rt == nil || manager == nil {
		return
	}
	rt.mu.Lock()
	_, done := rt.managers[manager]
	rt.managers[manager] = struct{}{}
	rt.mu.Unlock()
	if done {
		return
	}
	manager.SetAccountSlotCoordinator(accountSlotAdapter{store: rt.store, coord: rt.coord})
	if rt.store.Available() {
		manager.SetSessionAffinityStore(affinityAdapter{store: rt.store})
	}
	rt.relay.attach(manager)
}

// Close undoes Install. The account limiters keep their coordinator: slots
// still held are released through it, and after the store is closed those
// releases are no-ops.
func (rt *Runtime) Close() {
	if rt == nil {
		return
	}
	rt.relay.close()
	middleware.SetClusterQuota(nil)
	if rt.store.Available() {
		management.SetClusterThrottleStore(nil)
		ipaccess.SetClusterAutoBanStore(nil)
		executor.SetSharedIDMinter(nil)
	}
	rt.mu.Lock()
	for manager := range rt.managers {
		manager.SetSessionAffinityStore(nil)
	}
	rt.mu.Unlock()
	rt.store.Close()
}

// splitLimit is ceil(limit/nodes): each node's share of a limit when the
// shared counters are unavailable.
func splitLimit(limit, nodes int) int {
	if limit <= 0 || nodes <= 1 {
		return limit
	}
	return (limit + nodes - 1) / nodes
}

type quotaAdapter struct {
	*sharedstate.Store
	coord func() *cluster.Coordinator
}

func (a quotaAdapter) ActiveNodes() int { return a.coord().ActiveNodeCount() }

type accountSlotAdapter struct {
	store *sharedstate.Store
	coord func() *cluster.Coordinator
}

func (a accountSlotAdapter) Shared() bool { return a.store.Available() }

func (a accountSlotAdapter) NodeShare(limit int) int {
	return splitLimit(limit, a.coord().ActiveNodeCount())
}

func (a accountSlotAdapter) Acquire(authID string, limit int) (bool, error) {
	ok, _, err := a.store.AcquireAccountSlot(context.Background(), authID, limit)
	return ok, err
}

func (a accountSlotAdapter) Release(authID string) { a.store.ReleaseAccountSlot(authID) }

type affinityAdapter struct{ store *sharedstate.Store }

func (a affinityAdapter) Lookup(ctx context.Context, key string, ttl time.Duration) (coreauth.SessionAffinityBinding, error) {
	b, err := a.store.AffinityLookup(ctx, key, ttl)
	return coreauth.SessionAffinityBinding{AccountRef: b.Account, Served: b.Served, Found: b.Found}, err
}

func (a affinityAdapter) Bind(ctx context.Context, key, accountRef string, ttl time.Duration) (coreauth.SessionAffinityBinding, error) {
	b, err := a.store.AffinityBind(ctx, key, accountRef, ttl)
	return coreauth.SessionAffinityBinding{AccountRef: b.Account, Served: b.Served, Found: b.Found}, err
}

func (a affinityAdapter) Release(ctx context.Context, key, accountRef string) error {
	return a.store.AffinityRelease(ctx, key, accountRef)
}
