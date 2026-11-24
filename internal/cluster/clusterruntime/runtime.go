// Package clusterruntime wires the cluster-wide limits into a running node:
// it opens the shared state on the cluster database and installs the shared
// counters, slots, bindings and minting into the quota middleware, the login
// throttle, the auto-ban engine, the executors and the auth manager, and
// relays upstream cooldowns between nodes.
//
// Nothing here runs outside cluster mode: a single node keeps every
// pre-cluster code path.
package clusterruntime

import (
	"context"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster/sharedstate"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/ipaccess"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Runtime is one node's cluster wiring.
type Runtime struct {
	coord func() *cluster.Coordinator
	// pg holds the state shared through the cluster database; nil when the
	// cluster has no database.
	pg *sharedstate.PGStore
	// quota holds the PostgreSQL-backed per-subject windows; nil when the
	// cluster has no database.
	quota *pgQuotaAdapter
	// slots decides cluster-wide account concurrency.
	slots coreauth.AccountSlotCoordinator
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
// concurrency slots back to the cluster, flushes pending counters and closes
// the shared store; call it once the server has drained. nil is a no-op.
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

var (
	_ management.ClusterThrottleStore = (*sharedstate.PGStore)(nil)
	_ ipaccess.ClusterAutoBanStore    = (*sharedstate.PGStore)(nil)
	_ middleware.ClusterQuota         = (*pgQuotaAdapter)(nil)
)

// start builds the runtime. coord resolves the coordinator on every use.
func start(coord func() *cluster.Coordinator) *Runtime {
	c := coord()
	if !c.Enabled() {
		log.Warn("cluster: cluster mode is on but no cluster coordinator is running; cooldowns will not be shared")
	}
	nodeID := c.NodeID()

	var pg *sharedstate.PGStore
	var quota *pgQuotaAdapter
	var slots coreauth.AccountSlotCoordinator
	if pool := pgSlotPool(coord); pool != nil {
		pg = sharedstate.NewPGStore(pool)
		slots = newPGSlotAdapter(pool, coord, nodeID)
		quota = newPGQuotaAdapter(pool, coord, nodeID)
		executor.SetSharedIDMinter(pg)
		management.SetClusterThrottleStore(pg)
		ipaccess.SetClusterAutoBanStore(pg)
		log.Infof("cluster: slots, affinity, quota windows, synthetic ids, login throttles and auto-ban counters are shared through PostgreSQL")
	} else {
		slots = splitSlotAdapter{coord: coord}
		log.Warn("cluster: no cluster database; per-key and per-account limits are split across the active nodes, and login throttles and auto-bans stay per node")
	}
	return newRuntime(coord, pg, quota, slots)
}

func newRuntime(coord func() *cluster.Coordinator, pg *sharedstate.PGStore, quota *pgQuotaAdapter, slots coreauth.AccountSlotCoordinator) *Runtime {
	rt := &Runtime{
		coord:    coord,
		pg:       pg,
		quota:    quota,
		slots:    slots,
		relay:    newCooldownRelay(coord),
		managers: make(map[*coreauth.Manager]struct{}),
	}
	rt.relay.start(coord())

	// The quota middleware is cluster-aware even without shared state: it then
	// enforces each node's share of the limits. With a database it reads the
	// cluster-wide windows instead.
	middleware.SetClusterQuota(splitQuotaAdapter{coord: coord})
	if rt.quota != nil {
		middleware.SetClusterQuota(rt.quota)
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
	if rt.slots != nil {
		manager.SetAccountSlotCoordinator(rt.slots)
	}
	if rt.pg != nil && rt.pg.Available() {
		manager.SetSessionAffinityStore(pgAffinityAdapter{store: rt.pg})
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
	management.SetClusterThrottleStore(nil)
	ipaccess.SetClusterAutoBanStore(nil)
	executor.SetSharedIDMinter(nil)
	if rt.quota != nil {
		rt.quota.Close()
	}
	rt.mu.Lock()
	for manager := range rt.managers {
		manager.SetSessionAffinityStore(nil)
	}
	rt.mu.Unlock()
	if closer, ok := rt.slots.(interface{ Close() }); ok {
		closer.Close()
	}
}

// splitLimit is ceil(limit/nodes): each node's share of a limit when the
// shared counters are unavailable.
func splitLimit(limit, nodes int) int {
	if limit <= 0 || nodes <= 1 {
		return limit
	}
	return (limit + nodes - 1) / nodes
}

// splitQuotaAdapter is the quota middleware's view when the cluster has no
// shared counters: nothing is shared, so each node enforces its share.
type splitQuotaAdapter struct {
	coord func() *cluster.Coordinator
}

func (a splitQuotaAdapter) Available() bool  { return false }
func (a splitQuotaAdapter) ActiveNodes() int { return a.coord().ActiveNodeCount() }

func (a splitQuotaAdapter) AdmitRequest(ctx context.Context, subject string, concurrencyLimit int) (sharedstate.Admission, error) {
	return sharedstate.Admission{}, sharedstate.ErrUnavailable
}

func (a splitQuotaAdapter) CountRequest(subject string)            {}
func (a splitQuotaAdapter) AddTokens(subject string, tokens int64) {}
func (a splitQuotaAdapter) Rates(ctx context.Context, subjects []string) (map[string]sharedstate.Rate, error) {
	return nil, sharedstate.ErrUnavailable
}

// splitSlotAdapter enforces this node's share of an account's concurrency
// limit while there is no shared slot store.
type splitSlotAdapter struct {
	coord func() *cluster.Coordinator
}

func (a splitSlotAdapter) Shared() bool { return false }

func (a splitSlotAdapter) NodeShare(limit int) int {
	return splitLimit(limit, a.coord().ActiveNodeCount())
}

func (a splitSlotAdapter) Acquire(authID string, limit int) (bool, error) {
	return false, sharedstate.ErrUnavailable
}

func (a splitSlotAdapter) Release(authID string) {}

// pgAffinityAdapter maps sharedstate's binding onto the manager's store
// interface.
type pgAffinityAdapter struct{ store *sharedstate.PGStore }

func (a pgAffinityAdapter) Lookup(ctx context.Context, key string, ttl time.Duration) (coreauth.SessionAffinityBinding, error) {
	b, err := a.store.AffinityLookup(ctx, key, ttl)
	return coreauth.SessionAffinityBinding{AccountRef: b.Account, Served: b.Served, Found: b.Found}, err
}

func (a pgAffinityAdapter) Bind(ctx context.Context, key, accountRef string, ttl time.Duration) (coreauth.SessionAffinityBinding, error) {
	b, err := a.store.AffinityBind(ctx, key, accountRef, ttl)
	return coreauth.SessionAffinityBinding{AccountRef: b.Account, Served: b.Served, Found: b.Found}, err
}

func (a pgAffinityAdapter) Release(ctx context.Context, key, accountRef string) error {
	return a.store.AffinityRelease(ctx, key, accountRef)
}
