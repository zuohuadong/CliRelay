package egress

import (
	"context"
	"errors"
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

func TestServiceCheckEndpointUsesStrictProxyAndPersistsPublicIP(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.URL.String(); got != "http://probe.invalid/ip" {
			t.Errorf("proxy request URL = %q", got)
		}
		_, _ = w.Write([]byte(`{"ip":"198.51.100.44"}`))
	}))
	defer proxyServer.Close()
	host, port := splitTestServer(t, proxyServer.URL)

	service := newTestService(t, true)
	service.probeURLs = []string{"http://probe.invalid/ip"}
	endpoint, err := service.CreateEndpoint(context.Background(), Endpoint{
		Name: "Singapore", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.44",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	checked, err := service.CheckEndpoint(context.Background(), endpoint.ID)
	if err != nil {
		t.Fatalf("CheckEndpoint() error = %v", err)
	}
	if hits.Load() != 1 || checked.PublicIP != endpoint.ExpectedPublicIP || checked.CheckStatus != EndpointStatusHealthy || checked.LastCheckedAt.IsZero() {
		t.Fatalf("checked endpoint = %#v, hits=%d", checked, hits.Load())
	}
}

func TestServiceResolveUsesStableBindingAndNeverFailsOver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service := newTestService(t, true)
	now := service.now()
	endpointA, err := service.CreateEndpoint(ctx, Endpoint{Name: "A", Protocol: ProtocolSOCKS5, Host: "10.77.0.2", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.2"})
	if err != nil {
		t.Fatal(err)
	}
	endpointB, err := service.CreateEndpoint(ctx, Endpoint{Name: "B", Protocol: ProtocolSOCKS5, Host: "10.77.0.3", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.3"})
	if err != nil {
		t.Fatal(err)
	}
	endpointA, _ = service.store.UpdateEndpointCheck(ctx, endpointA.ID, endpointA.ExpectedPublicIP, EndpointStatusHealthy, "", 1, now)
	endpointB, _ = service.store.UpdateEndpointCheck(ctx, endpointB.ID, endpointB.ExpectedPublicIP, EndpointStatusHealthy, "", 1, now)
	identity, _ := StableIdentity("acct-A")
	if err = service.store.PutBinding(ctx, Binding{Identity: identity, EndpointID: endpointA.ID}); err != nil {
		t.Fatal(err)
	}

	resolved, err := service.Resolve(ctx, "acct-A")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Endpoint.ID != endpointA.ID || resolved.ProxyURL != "socks5://10.77.0.2:1080" {
		t.Fatalf("Resolve() = %#v", resolved)
	}
	_, _ = service.store.UpdateEndpointCheck(ctx, endpointA.ID, "", EndpointStatusUnhealthy, "network down", 1, now)
	if _, err = service.Resolve(ctx, "acct-A"); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("Resolve() unhealthy bound endpoint error = %v", err)
	}
	resolvedB, err := service.ResolveEndpoint(ctx, endpointB.ID)
	if err != nil || resolvedB.Endpoint.ID != endpointB.ID {
		t.Fatalf("endpoint B should remain healthy for explicit management lookup: %#v, %v", resolvedB, err)
	}
}

func TestServiceResolveFailsClosedWhenDisabledUnboundOrStale(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	disabled := newTestService(t, false)
	if _, err := disabled.Resolve(ctx, "acct"); !errors.Is(err, ErrEgressRequired) {
		t.Fatalf("disabled Resolve() error = %v", err)
	}
	enabled := newTestService(t, true)
	if _, err := enabled.Resolve(ctx, "acct"); !errors.Is(err, ErrEgressRequired) {
		t.Fatalf("unbound Resolve() error = %v", err)
	}
	endpoint, err := enabled.CreateEndpoint(ctx, Endpoint{Name: "stale", Protocol: ProtocolSOCKS5, Host: "10.77.0.2", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.2"})
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := StableIdentity("acct")
	if err = enabled.store.PutBinding(ctx, Binding{Identity: identity, EndpointID: endpoint.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = enabled.Resolve(ctx, "acct"); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("stale Resolve() error = %v", err)
	}
}

func TestServiceCheckEndpointPersistsMismatchAndRejectsOverlappingCheck(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"ip":"198.51.100.9"}`))
	}))
	defer proxyServer.Close()
	host, port := splitTestServer(t, proxyServer.URL)
	service := newTestService(t, true)
	service.probeURLs = []string{"http://probe.invalid/ip"}
	endpoint, err := service.CreateEndpoint(context.Background(), Endpoint{Name: "mismatch", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.8"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, checkErr := service.CheckEndpoint(context.Background(), endpoint.ID)
		done <- checkErr
	}()
	<-started
	if _, err = service.CheckEndpoint(context.Background(), endpoint.ID); !errors.Is(err, ErrCheckInProgress) {
		t.Fatalf("overlapping CheckEndpoint() error = %v", err)
	}
	close(release)
	if err = <-done; !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("mismatch CheckEndpoint() error = %v", err)
	}
	persisted, err := service.GetEndpoint(context.Background(), endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.CheckStatus != EndpointStatusIPMismatch || persisted.PublicIP != "198.51.100.9" {
		t.Fatalf("persisted mismatch = %#v", persisted)
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
	host, port := splitTestServer(t, proxyServer.URL)
	service := newTestService(t, true)
	service.probeURLs = []string{"http://probe.invalid/ip"}
	ctx := context.Background()
	endpoint, err := service.CreateEndpoint(ctx, Endpoint{Name: "one", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, checkErr := service.CheckEndpoint(ctx, endpoint.ID)
		done <- checkErr
	}()
	<-started
	endpoint.Username = "rotated-user"
	if _, err = service.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err = <-done; !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("CheckEndpoint() error = %v", err)
	}
	persisted, _ := service.GetEndpoint(ctx, endpoint.ID)
	if persisted.Username != "rotated-user" || persisted.PublicIP != "" || persisted.CheckStatus != "" {
		t.Fatalf("stale probe overwrote route: %#v", persisted)
	}
}

func TestServiceSetConfigReloadsProbeURLs(t *testing.T) {
	t.Parallel()
	service := newTestService(t, true)
	service.SetConfig(&config.Config{EgressNetwork: config.EgressNetworkConfig{
		Enabled: true, ProbeURLs: []string{"https://probe.example/ip"},
	}})
	if got := service.endpointProbeURLs(); len(got) != 1 || got[0] != "https://probe.example/ip" {
		t.Fatalf("endpointProbeURLs() = %#v", got)
	}
}

func newTestService(t *testing.T, enabled bool) *Service {
	t.Helper()
	cfg := &config.Config{EgressNetwork: config.EgressNetworkConfig{
		Enabled: enabled, EndpointCheckInterval: "1h", EndpointHealthTTL: "2h",
	}}
	service, err := NewService(cfg, filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	service.now = func() time.Time { return time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC) }
	return service
}

func splitTestServer(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi() error = %v", err)
	}
	return host, port
}
