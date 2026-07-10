package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestServiceResolveRejectsStaleNodeAndStaleEndpointHealth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := newTestService(t, true)
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	if err := service.store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{"100.64.0.1"}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, base); err != nil {
		t.Fatal(err)
	}
	endpoint, err := service.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.1", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = service.store.UpdateEndpointCheck(ctx, endpoint.ID, endpoint.ExpectedPublicIP, EndpointStatusHealthy, "", 1, base)
	identity, _ := StableIdentity("acct-1")
	if err = service.store.PutBinding(ctx, Binding{Identity: identity, EndpointID: endpoint.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Resolve(ctx, "acct-1"); err != nil {
		t.Fatalf("fresh Resolve() error = %v", err)
	}
	service.now = func() time.Time { return base.Add(4 * time.Minute) }
	if _, err = service.Resolve(ctx, "acct-1"); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("stale node Resolve() error = %v", err)
	}
	if err = service.store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{"100.64.0.1"}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, base.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return base.Add(6 * time.Minute) }
	if _, err = service.Resolve(ctx, "acct-1"); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("stale health Resolve() error = %v", err)
	}
}

func TestServiceCheckPersistsExpectedIPMismatch(t *testing.T) {
	t.Parallel()
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ip":"198.51.100.2"}`))
	}))
	defer proxyServer.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	service := newTestService(t, true)
	service.probeURLs = []string{"http://probe.invalid/ip"}
	ctx := context.Background()
	_ = service.store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, service.now())
	endpoint, err := service.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	if err != nil {
		t.Fatal(err)
	}
	checked, err := service.CheckEndpoint(ctx, endpoint.ID)
	if !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("CheckEndpoint() error = %v", err)
	}
	if checked.PublicIP != "198.51.100.2" || checked.CheckStatus != EndpointStatusIPMismatch || checked.CheckError == "" {
		t.Fatalf("mismatch check = %#v", checked)
	}
}

func TestServiceCheckDropsResultWhenEndpointRouteChangesInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"ip":"198.51.100.1"}`))
	}))
	defer proxyServer.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	service := newTestService(t, true)
	service.probeURLs = []string{"http://probe.invalid/ip"}
	ctx := context.Background()
	if err := service.store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, service.now()); err != nil {
		t.Fatal(err)
	}
	endpoint, err := service.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, checkErr := service.CheckEndpoint(ctx, endpoint.ID)
		result <- checkErr
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for endpoint probe")
	}
	endpoint.Username = "rotated-user"
	if _, err = service.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatalf("UpdateEndpoint() error = %v", err)
	}
	close(release)
	if err = <-result; !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("CheckEndpoint() error = %v, want ErrRevisionConflict", err)
	}
	persisted, err := service.GetEndpoint(ctx, endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Username != "rotated-user" || persisted.PublicIP != "" || persisted.CheckStatus != "" || !persisted.LastCheckedAt.IsZero() {
		t.Fatalf("stale probe overwrote updated route: %#v", persisted)
	}
}

func TestServiceResolveRejectsDuplicateObservedPublicIP(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := newTestService(t, true)
	now := service.now()
	_ = service.store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{"100.64.0.1", "100.64.0.2"}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, now)
	e1, _ := service.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.1", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	e2, _ := service.CreateEndpoint(ctx, Endpoint{Name: "two", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.2", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.2"})
	_, _ = service.store.UpdateEndpointCheck(ctx, e1.ID, "198.51.100.1", EndpointStatusHealthy, "", 1, now)
	_, _ = service.store.UpdateEndpointCheck(ctx, e2.ID, "198.51.100.1", EndpointStatusHealthy, "", 1, now)
	identity, _ := StableIdentity("acct-1")
	_ = service.store.PutBinding(ctx, Binding{Identity: identity, EndpointID: e1.ID})
	readiness, err := service.EndpointReadiness(ctx, e1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.DuplicatePublicIP || readiness.RuntimeReady {
		t.Fatalf("readiness = %#v", readiness)
	}
	if _, err = service.Resolve(ctx, "acct-1"); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestServiceLifecycleSyncsImmediatelyAndWakesOnConfig(t *testing.T) {
	var firstHits, secondHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstHits.Add(1)
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHits.Add(1)
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	defer second.Close()
	t.Setenv("EGRESS_TEST_KEY", "secret")
	cfg := lifecycleConfig(first.URL)
	service, err := NewService(cfg, filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return firstHits.Load() > 0 })
	service.SetConfig(lifecycleConfig(second.URL))
	waitFor(t, func() bool { return secondHits.Load() > 0 })
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceLifecycleKeepsNodeSyncIndependentAndBoundsEndpointChecks(t *testing.T) {
	var active, peak, syncHits atomic.Int32
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"ip":"198.51.100.1"}`))
	}))
	defer proxyServer.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	headscaleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		syncHits.Add(1)
		_, _ = fmt.Fprintf(w, `{"nodes":[{"id":"n1","givenName":"n1","ipAddresses":[%q],"online":true,"tags":[%q]}]}`, host, config.DefaultEgressServiceTag)
	}))
	defer headscaleServer.Close()
	t.Setenv("EGRESS_LIFECYCLE_KEY", "secret")
	cfg := &config.Config{EgressNetwork: config.EgressNetworkConfig{
		Enabled: true, EndpointCheckInterval: "1h", EndpointHealthTTL: "2h",
		Headscale: config.HeadscaleConfig{URL: headscaleServer.URL, APIKeyEnv: "EGRESS_LIFECYCLE_KEY", ServiceTag: config.DefaultEgressServiceTag, SyncInterval: "1h", NodeFreshnessTTL: "2h"},
	}}
	service, err := NewService(cfg, filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	service.probeURLs = []string{"http://probe.invalid/ip"}
	ctx := context.Background()
	_ = service.store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, service.now())
	for i := 0; i < 5; i++ {
		_, err = service.CreateEndpoint(ctx, Endpoint{Name: fmt.Sprintf("endpoint-%d", i), NodeID: "n1", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: fmt.Sprintf("198.51.100.%d", i+1)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for bounded concurrent checks")
		}
	}
	waitFor(t, func() bool { return syncHits.Load() > 0 })
	if got := peak.Load(); got > 4 {
		t.Fatalf("endpoint check peak=%d, want <=4", got)
	}
	close(release)
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRejectsOverlappingCheckForSameEndpoint(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"ip":"198.51.100.1"}`))
	}))
	defer proxyServer.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	service := newTestService(t, true)
	service.probeURLs = []string{"http://probe.invalid/ip"}
	ctx := context.Background()
	_ = service.store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, service.now())
	endpoint, _ := service.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	done := make(chan error, 1)
	go func() {
		_, checkErr := service.CheckEndpoint(ctx, endpoint.ID)
		done <- checkErr
	}()
	<-started
	if _, err := service.CheckEndpoint(ctx, endpoint.ID); !errors.Is(err, ErrCheckInProgress) {
		t.Fatalf("overlapping CheckEndpoint() error=%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first CheckEndpoint() error=%v", err)
	}
}

func TestSyncFailurePreservesLastSuccessfulTimestamp(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	defer server.Close()
	t.Setenv("EGRESS_TEST_KEY", "secret")
	service, err := NewService(lifecycleConfig(server.URL), filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	if _, err = service.SyncNodes(context.Background()); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	service.now = func() time.Time { return base.Add(time.Hour) }
	if _, err = service.SyncNodes(context.Background()); err == nil {
		t.Fatal("SyncNodes() error = nil")
	}
	state, err := service.SyncState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastSync.Equal(base) || state.Error == "" {
		t.Fatalf("sync state = %#v", state)
	}
}

func TestServiceCloseCancelsAndWaitsForLifecycle(t *testing.T) {
	started := make(chan struct{})
	exited := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(exited)
	}))
	defer server.Close()
	t.Setenv("EGRESS_TEST_KEY", "secret")
	service, err := NewService(lifecycleConfig(server.URL), filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("immediate sync did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- service.Close() }()
	select {
	case err = <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not wait/cancel lifecycle promptly")
	}
	select {
	case <-exited:
	default:
		t.Fatal("Headscale request was not canceled before Close returned")
	}
}

func lifecycleConfig(url string) *config.Config {
	return &config.Config{EgressNetwork: config.EgressNetworkConfig{
		EndpointCheckInterval: "1h",
		EndpointHealthTTL:     "2h",
		Headscale: config.HeadscaleConfig{
			URL: url, APIKeyEnv: "EGRESS_TEST_KEY", ServiceTag: config.DefaultEgressServiceTag,
			SyncInterval: "1h", NodeFreshnessTTL: "2h",
		},
	}}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
