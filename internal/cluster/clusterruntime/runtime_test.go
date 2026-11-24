package clusterruntime

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newManager(t *testing.T, ids ...string) *coreauth.Manager {
	t.Helper()
	m := coreauth.NewManager(nil, nil, nil)
	for _, id := range ids {
		if _, err := m.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func blockedFor(m *coreauth.Manager, authID, model string) bool {
	auth, ok := m.GetByID(authID)
	if !ok {
		return false
	}
	if state := auth.ModelStates[model]; state != nil && state.Unavailable && state.NextRetryAfter.After(time.Now()) {
		return true
	}
	return auth.Unavailable && auth.NextRetryAfter.After(time.Now())
}

// relayNode is one node of an in-memory cluster: its coordinator, relay and
// auth manager.
type relayNode struct {
	coord   *cluster.Coordinator
	relay   *cooldownRelay
	manager *coreauth.Manager
}

func joinRelay(t *testing.T, hub *cluster.MemoryHub, nodeID string) *relayNode {
	t.Helper()
	coord := hub.Join(nodeID)
	relay := newCooldownRelay(func() *cluster.Coordinator { return coord })
	relay.start(coord)
	manager := newManager(t, "acct-1", "acct-2")
	relay.attach(manager)
	t.Cleanup(relay.close)
	return &relayNode{coord: coord, relay: relay, manager: manager}
}

func TestCooldownRelayCarriesCooldownsBetweenNodes(t *testing.T) {
	hub := cluster.NewMemoryHub()
	a, b := joinRelay(t, hub, "node-a"), joinRelay(t, hub, "node-b")

	retry := 10 * time.Minute
	a.manager.MarkResult(context.Background(), coreauth.Result{
		AuthID: "acct-1", Provider: "codex", Model: "gpt-5.5", RetryAfter: &retry,
		Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota_exhausted"},
	})
	eventually(t, "node b to learn node a's cooldown", func() bool { return blockedFor(b.manager, "acct-1", "gpt-5.5") })

	auth, _ := b.manager.GetByID("acct-1")
	state := auth.ModelStates["gpt-5.5"]
	if !state.Quota.Exceeded || state.LastError == nil || state.LastError.HTTPStatus != 429 {
		t.Fatalf("the relayed cooldown must carry the quota flag and status: %+v", state)
	}
	if blockedFor(b.manager, "acct-2", "gpt-5.5") {
		t.Fatal("other accounts must stay usable")
	}
}

func TestCooldownRelayDeduplicatesSmallExtensions(t *testing.T) {
	hub := cluster.NewMemoryHub()
	a := joinRelay(t, hub, "node-a")
	observer := hub.Join("observer")
	var received atomic.Int64
	observer.Subscribe(cluster.TopicCooldown, func(ev cluster.Event) {
		if !ev.Resync {
			received.Add(1)
		}
	})

	now := time.Now()
	a.relay.publish(coreauth.CooldownNotice{AuthID: "acct-1", Model: "m", Until: now.Add(time.Minute)})
	a.relay.publish(coreauth.CooldownNotice{AuthID: "acct-1", Model: "m", Until: now.Add(time.Minute + 3*time.Second)})
	eventually(t, "the first event", func() bool { return received.Load() == 1 })
	a.relay.publish(coreauth.CooldownNotice{AuthID: "acct-1", Model: "m", Until: now.Add(2 * time.Minute)})
	a.relay.publish(coreauth.CooldownNotice{AuthID: "acct-1", Model: "other", Until: now.Add(time.Minute)})
	eventually(t, "the extension and the other model", func() bool { return received.Load() == 3 })
	time.Sleep(50 * time.Millisecond)
	if n := received.Load(); n != 3 {
		t.Fatalf("an extension of 3s must not be re-announced: got %d events", n)
	}
}

func TestCooldownRelayResyncIsANoOp(t *testing.T) {
	hub := cluster.NewMemoryHub()
	b := joinRelay(t, hub, "node-b")
	hub.Disconnect("node-b")
	hub.Reconnect("node-b") // delivers a Resync to every subscriber
	time.Sleep(20 * time.Millisecond)
	if blockedFor(b.manager, "acct-1", "gpt-5.5") {
		t.Fatal("a resync must not change any cooldown")
	}
	if len(b.relay.inbound) != 0 {
		t.Fatal("a resync must not even be queued")
	}
}

func TestCooldownRelaySilentOnSingleNode(t *testing.T) {
	single := cluster.Default()
	relay := newCooldownRelay(func() *cluster.Coordinator { return single })
	relay.start(single)
	t.Cleanup(relay.close)
	relay.publish(coreauth.CooldownNotice{AuthID: "acct-1", Until: time.Now().Add(time.Minute)})
	relay.mu.Lock()
	claimed := len(relay.published)
	relay.mu.Unlock()
	if len(relay.outbound) != 0 || claimed != 0 {
		t.Fatal("a single node has no peers to tell")
	}
}

func TestInstallIsANoOpOnASingleNode(t *testing.T) {
	cfg := &config.Config{}
	if rt := Install(cfg, newManager(t)); rt != nil {
		t.Fatal("cluster mode off must install nothing")
	}
	Release(nil) // nothing to release
}

func TestInstallWiresAndShutdownUnwires(t *testing.T) {
	hub := cluster.NewMemoryHub()
	cluster.SetDefault(hub.Join("node-a"))
	t.Cleanup(func() { cluster.SetDefault(nil) })

	cfg := &config.Config{}
	cfg.Cluster.Enabled = true
	manager := newManager(t, "acct-1")
	rt := Install(cfg, manager)
	if rt == nil || Install(cfg, newManager(t)) != rt {
		t.Fatal("install must share one runtime per process")
	}
	// Without a cluster database there is nothing to share through it.
	if rt.pg != nil || rt.quota != nil {
		t.Fatalf("no database means no shared store: %+v", rt)
	}

	// The first release leaves the runtime to the other holder.
	Release(rt)
	Release(rt)
	if Install(&config.Config{}, manager) != nil {
		t.Fatal("after release a single-node config installs nothing")
	}
}

func TestInstallWithoutDatabaseStillSplitsLimits(t *testing.T) {
	hub := cluster.NewMemoryHub()
	coordA := hub.Join("node-a")
	hub.Join("node-b")
	cluster.SetDefault(coordA)
	t.Cleanup(func() { cluster.SetDefault(nil) })

	cfg := &config.Config{}
	cfg.Cluster.Enabled = true
	manager := newManager(t)
	rt := Install(cfg, manager)
	t.Cleanup(func() { Release(rt) })
	if rt == nil || rt.pg != nil {
		t.Fatal("no cluster database means no shared store")
	}
	limited := &coreauth.Auth{ID: "acct-9", Attributes: map[string]string{"concurrency_limit": "4"}}
	for i := 0; i < 2; i++ {
		if _, err := manager.ConcurrencyLimiter().AcquireSlot(limited); err != nil {
			t.Fatalf("slot %d of the node share: %v", i+1, err)
		}
	}
	if _, err := manager.ConcurrencyLimiter().AcquireSlot(limited); err == nil {
		t.Fatal("two active nodes split a limit of 4 into 2 per node")
	}
}
