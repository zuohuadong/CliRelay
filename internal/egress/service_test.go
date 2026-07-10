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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestServiceRejectsRemoteEndpointOutsideSelectedNodeAddresses(t *testing.T) {
	t.Parallel()

	service := newTestService(t, true)
	ctx := context.Background()
	if err := service.store.UpsertNodes(ctx, []Node{{
		ID:        "17",
		Name:      "sg-01",
		Addresses: []string{"100.64.0.17", "fd7a:115c:a1e0::17"},
		Online:    true,
		Tags:      []string{config.DefaultEgressServiceTag},
	}}, service.now()); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}

	_, err := service.CreateEndpoint(ctx, Endpoint{
		Name:             "public proxy escape",
		NodeID:           "17",
		Protocol:         ProtocolSOCKS5,
		Host:             "203.0.113.10",
		Port:             1080,
		Enabled:          true,
		ExpectedPublicIP: "198.51.100.10",
	})
	if !errors.Is(err, ErrEndpointInvalid) {
		t.Fatalf("CreateEndpoint() error = %v, want ErrEndpointInvalid", err)
	}
}

func TestServiceCheckEndpointUsesProxyAndPersistsPublicIP(t *testing.T) {
	t.Parallel()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.String(); got != "http://probe.invalid/ip" {
			t.Fatalf("proxy request URL = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"198.51.100.44"}`))
	}))
	defer proxyServer.Close()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	port, _ := strconv.Atoi(portText)

	service := newTestService(t, true)
	service.probeURLs = []string{"http://probe.invalid/ip"}
	ctx := context.Background()
	if err = service.store.UpsertNodes(ctx, []Node{{
		ID: "17", Name: "probe-node", Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag},
	}}, service.now()); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}
	endpoint, err := service.CreateEndpoint(ctx, Endpoint{
		Name: "probe endpoint", NodeID: "17", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.44",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}

	checked, err := service.CheckEndpoint(ctx, endpoint.ID)
	if err != nil {
		t.Fatalf("CheckEndpoint() error = %v", err)
	}
	if checked.PublicIP != "198.51.100.44" || checked.CheckStatus != "healthy" || checked.LatencyMS < 0 || checked.LastCheckedAt.IsZero() {
		t.Fatalf("CheckEndpoint() = %#v", checked)
	}
	persisted, err := service.store.GetEndpoint(ctx, endpoint.ID)
	if err != nil {
		t.Fatalf("GetEndpoint() error = %v", err)
	}
	if persisted.PublicIP != checked.PublicIP || persisted.CheckStatus != "healthy" {
		t.Fatalf("persisted endpoint = %#v", persisted)
	}
}

func TestServiceCheckEndpointUsesProxyWhileRuntimeIsDisabled(t *testing.T) {
	t.Parallel()

	var proxyHits atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if got := r.URL.String(); got != "http://probe.invalid/ip" {
			t.Errorf("proxy request URL = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"198.51.100.45"}`))
	}))
	defer proxyServer.Close()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	port, _ := strconv.Atoi(portText)

	service := newTestService(t, false)
	service.probeURLs = []string{"http://probe.invalid/ip"}
	ctx := context.Background()
	if err = service.store.UpsertNodes(ctx, []Node{{
		ID: "18", Name: "disabled-runtime-probe", Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag},
	}}, service.now()); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}
	endpoint, err := service.CreateEndpoint(ctx, Endpoint{
		Name: "disabled runtime probe", NodeID: "18", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.45",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}

	checked, err := service.CheckEndpoint(ctx, endpoint.ID)
	if err != nil {
		t.Fatalf("CheckEndpoint() error = %v", err)
	}
	if got := proxyHits.Load(); got != 1 {
		t.Fatalf("proxy hits = %d, want 1", got)
	}
	if checked.PublicIP != "198.51.100.45" || checked.CheckStatus != "healthy" {
		t.Fatalf("CheckEndpoint() = %#v", checked)
	}
	if _, err = service.ResolveEndpoint(ctx, endpoint.ID); !errors.Is(err, ErrEgressRequired) {
		t.Fatalf("ResolveEndpoint() error = %v, want ErrEgressRequired", err)
	}
}

func TestServiceCheckEndpointFallsBackAcrossStrictProxyProbes(t *testing.T) {
	var hits atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/first":
			w.WriteHeader(http.StatusBadGateway)
		case "/second":
			_, _ = w.Write([]byte(`{"ip":"198.51.100.44"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer proxyServer.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	service := newTestService(t, true)
	service.probeURLs = []string{"http://probe.invalid/first", "http://probe.invalid/second"}
	ctx := context.Background()
	_ = service.store.UpsertNodes(ctx, []Node{{ID: "17", Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, service.now())
	endpoint, _ := service.CreateEndpoint(ctx, Endpoint{Name: "fallback", NodeID: "17", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.44"})
	checked, err := service.CheckEndpoint(ctx, endpoint.ID)
	if err != nil || checked.CheckStatus != EndpointStatusHealthy || checked.PublicIP != endpoint.ExpectedPublicIP || hits.Load() != 2 {
		t.Fatalf("checked=%#v hits=%d err=%v", checked, hits.Load(), err)
	}
}

func TestServiceCheckEndpointMarksUnhealthyWhenAllProbesFail(t *testing.T) {
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/invalid" {
			_, _ = w.Write([]byte("not-an-ip"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer proxyServer.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	service := newTestService(t, true)
	service.probeURLs = []string{"http://probe.invalid/fail", "http://probe.invalid/invalid"}
	ctx := context.Background()
	_ = service.store.UpsertNodes(ctx, []Node{{ID: "17", Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, service.now())
	endpoint, _ := service.CreateEndpoint(ctx, Endpoint{Name: "failed", NodeID: "17", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.44"})
	checked, err := service.CheckEndpoint(ctx, endpoint.ID)
	if err == nil || checked.CheckStatus != EndpointStatusUnhealthy || !strings.Contains(checked.CheckError, "all public IP probes failed") {
		t.Fatalf("checked=%#v err=%v", checked, err)
	}
}

func TestServiceCheckEndpointRecordsAllValidMismatches(t *testing.T) {
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/one" {
			_, _ = w.Write([]byte(`{"ip":"198.51.100.2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ip":"198.51.100.3"}`))
	}))
	defer proxyServer.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	service := newTestService(t, true)
	service.probeURLs = []string{"http://probe.invalid/one", "http://probe.invalid/two"}
	ctx := context.Background()
	_ = service.store.UpsertNodes(ctx, []Node{{ID: "17", Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, service.now())
	endpoint, _ := service.CreateEndpoint(ctx, Endpoint{Name: "mismatch", NodeID: "17", Protocol: ProtocolHTTP, Host: host, Port: port, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	checked, err := service.CheckEndpoint(ctx, endpoint.ID)
	if !errors.Is(err, ErrEndpointDisabled) || checked.CheckStatus != EndpointStatusIPMismatch || !strings.Contains(checked.CheckError, "198.51.100.2,198.51.100.3") {
		t.Fatalf("checked=%#v err=%v", checked, err)
	}
}

func TestServiceSetConfigReloadsProbeURLs(t *testing.T) {
	service := newTestService(t, true)
	service.SetConfig(&config.Config{EgressNetwork: config.EgressNetworkConfig{
		Enabled: true, ProbeURLs: []string{"https://probe-one.example/ip", "https://probe-two.example/ip"},
	}})
	if got := service.endpointProbeURLs(); len(got) != 2 || got[0] != "https://probe-one.example/ip" || got[1] != "https://probe-two.example/ip" {
		t.Fatalf("probe URLs after reload = %#v", got)
	}
}

func TestServiceResolveIsFailClosed(t *testing.T) {
	t.Parallel()

	service := newTestService(t, true)
	ctx := context.Background()
	if _, err := service.Resolve(ctx, "acct-123"); !errors.Is(err, ErrEgressRequired) {
		t.Fatalf("Resolve() missing binding error = %v", err)
	}

	if err := service.store.UpsertNodes(ctx, []Node{{
		ID:        "17",
		Name:      "sg-01",
		Addresses: []string{"100.64.0.17"},
		Online:    true,
		Tags:      []string{config.DefaultEgressServiceTag},
	}}, service.now()); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}
	endpoint, err := service.CreateEndpoint(ctx, Endpoint{
		Name:     "sg socks",
		NodeID:   "17",
		Protocol: ProtocolSOCKS5,
		Host:     "100.64.0.17",
		Port:     1080,
		Enabled:  false,
		Username: "relay user",
		Password: "secret/value",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	if _, err = service.ResolveEndpoint(ctx, endpoint.ID); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("ResolveEndpoint() disabled error = %v", err)
	}

	endpoint.Enabled = true
	endpoint.ExpectedPublicIP = "198.51.100.17"
	if _, err = service.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatalf("UpdateEndpoint() error = %v", err)
	}
	if _, err = service.store.UpdateEndpointCheck(ctx, endpoint.ID, endpoint.ExpectedPublicIP, EndpointStatusHealthy, "", 1, service.now()); err != nil {
		t.Fatalf("UpdateEndpointCheck() error = %v", err)
	}
	identity, _ := StableIdentity("acct-123")
	if err = service.store.PutBinding(ctx, Binding{Identity: identity, EndpointID: endpoint.ID}); err != nil {
		t.Fatalf("PutBinding() error = %v", err)
	}
	resolved, err := service.Resolve(ctx, "acct-123")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, want := resolved.ProxyURL, "socks5://relay%20user:secret%2Fvalue@100.64.0.17:1080"; got != want {
		t.Fatalf("ProxyURL = %q, want %q", got, want)
	}

	if err = service.store.UpsertNodes(ctx, nil, service.now()); err != nil {
		t.Fatalf("mark node stale: %v", err)
	}
	if _, err = service.Resolve(ctx, "acct-123"); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("Resolve() stale node error = %v", err)
	}
}

func TestServiceDisabledNeverRestoresDirectCodexAccess(t *testing.T) {
	t.Parallel()

	service := newTestService(t, false)
	if _, err := service.Resolve(context.Background(), "acct-123"); !errors.Is(err, ErrEgressRequired) {
		t.Fatalf("Resolve() error = %v, want ErrEgressRequired", err)
	}
}

func TestServiceAllowsBindingPreparationWhileRuntimeIsDisabled(t *testing.T) {
	t.Parallel()

	service := newTestService(t, false)
	ctx := context.Background()
	if err := service.store.UpsertNodes(ctx, []Node{{
		ID:        "17",
		Name:      "sg-01",
		Addresses: []string{"100.64.0.17"},
		Online:    true,
		Tags:      []string{config.DefaultEgressServiceTag},
	}}, service.now()); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}

	endpoint, err := service.CreateEndpoint(ctx, Endpoint{
		Name:             "migration-ready socks",
		NodeID:           "17",
		Protocol:         ProtocolSOCKS5,
		Host:             "100.64.0.17",
		Port:             1080,
		Enabled:          true,
		ExpectedPublicIP: "198.51.100.17",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	if _, err = service.store.UpdateEndpointCheck(ctx, endpoint.ID, endpoint.ExpectedPublicIP, EndpointStatusHealthy, "", 1, service.now()); err != nil {
		t.Fatalf("prepare endpoint health error = %v", err)
	}
	identity, err := StableIdentity("acct-migration")
	if err != nil {
		t.Fatalf("StableIdentity() error = %v", err)
	}
	if err = service.PutBinding(ctx, Binding{Identity: identity, EndpointID: endpoint.ID}); err != nil {
		t.Fatalf("PutBinding() while disabled error = %v", err)
	}
	if _, err = service.store.ResolveIdentity(ctx, identity); err != nil {
		t.Fatalf("prepared binding was not persisted: %v", err)
	}

	if _, err = service.Resolve(ctx, "acct-migration"); !errors.Is(err, ErrEgressRequired) {
		t.Fatalf("Resolve() error = %v, want ErrEgressRequired", err)
	}
	if _, err = service.ResolveEndpoint(ctx, endpoint.ID); !errors.Is(err, ErrEgressRequired) {
		t.Fatalf("ResolveEndpoint() error = %v, want ErrEgressRequired", err)
	}
	if _, err = service.HTTPClient(ctx, endpoint.ID, 0); !errors.Is(err, ErrEgressRequired) {
		t.Fatalf("HTTPClient() error = %v, want ErrEgressRequired", err)
	}

	disabledEndpoint, err := service.CreateEndpoint(ctx, Endpoint{
		Name:     "disabled socks",
		NodeID:   "17",
		Protocol: ProtocolSOCKS5,
		Host:     "100.64.0.17",
		Port:     1081,
		Enabled:  false,
	})
	if err != nil {
		t.Fatalf("CreateEndpoint(disabled) error = %v", err)
	}
	if err = service.PutBinding(ctx, Binding{Identity: identity, EndpointID: disabledEndpoint.ID}); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("PutBinding(disabled endpoint) error = %v, want ErrEndpointDisabled", err)
	}
	if err = service.PutBinding(ctx, Binding{Identity: identity, EndpointID: "missing"}); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("PutBinding(missing endpoint) error = %v, want ErrEndpointNotFound", err)
	}
}

func TestServiceAllowsDisablingEndpointWhileNodeIsOffline(t *testing.T) {
	t.Parallel()

	service := newTestService(t, true)
	ctx := context.Background()
	if err := service.store.UpsertNodes(ctx, []Node{{
		ID: "17", Name: "sg-01", Addresses: []string{"100.64.0.17"}, Online: true, Tags: []string{config.DefaultEgressServiceTag},
	}}, service.now()); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}
	endpoint, err := service.CreateEndpoint(ctx, Endpoint{
		Name: "sg socks", NodeID: "17", Protocol: ProtocolSOCKS5, Host: "100.64.0.17", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.17",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	if err = service.store.UpsertNodes(ctx, nil, service.now()); err != nil {
		t.Fatalf("mark node offline: %v", err)
	}
	endpoint.Enabled = false
	if _, err = service.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatalf("UpdateEndpoint(disable) error = %v", err)
	}
	endpoint.Enabled = true
	if _, err = service.UpdateEndpoint(ctx, endpoint); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("UpdateEndpoint(enable) error = %v, want ErrEndpointDisabled", err)
	}
}

func TestServiceBindingBatchRequiresRuntimeReadyEndpointInPreparationMode(t *testing.T) {
	t.Parallel()
	service := newTestService(t, false)
	ctx := context.Background()
	if err := service.store.UpsertNodes(ctx, []Node{{
		ID: "17", Name: "sg-01", Addresses: []string{"100.64.0.17"}, Online: true, Tags: []string{config.DefaultEgressServiceTag},
	}}, service.now()); err != nil {
		t.Fatal(err)
	}
	endpoint, err := service.CreateEndpoint(ctx, Endpoint{
		Name: "unchecked", NodeID: "17", Protocol: ProtocolSOCKS5, Host: "100.64.0.17", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.17",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := StableIdentity("unchecked")
	assignment := BindingAssignment{Identity: identity, EndpointID: endpoint.ID}
	preview, err := service.PreviewBindingBatch(ctx, []BindingAssignment{assignment})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Valid || len(preview.Conflicts) == 0 || preview.Conflicts[0].Code != "endpoint_not_ready" {
		t.Fatalf("unchecked preview = %#v", preview)
	}
	if _, err = service.ApplyBindingBatch(ctx, preview.Revision, preview.Assignments); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatalf("unchecked apply error = %v", err)
	}
	if _, err = service.store.UpdateEndpointCheck(ctx, endpoint.ID, endpoint.ExpectedPublicIP, EndpointStatusHealthy, "", 1, service.now()); err != nil {
		t.Fatal(err)
	}
	preview, err = service.PreviewBindingBatch(ctx, []BindingAssignment{assignment})
	if err != nil || !preview.Valid {
		t.Fatalf("checked preview = %#v, %v", preview, err)
	}
	if _, err = service.ApplyBindingBatch(ctx, preview.Revision, preview.Assignments); err != nil {
		t.Fatalf("checked apply error = %v", err)
	}
}

func TestServiceSyncFiltersTagAndMarksRemovedNodesOffline(t *testing.T) {
	t.Parallel()

	serverResponse := `{"nodes":[
  {"id":"17","givenName":"sg-01","ipAddresses":["100.64.0.17"],"online":true,"tags":["tag:clirelay-egress"]},
  {"id":"18","givenName":"other","ipAddresses":["100.64.0.18"],"online":true,"tags":["tag:other"]}
]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(serverResponse))
	}))
	defer server.Close()

	cfg := &config.Config{EgressNetwork: config.EgressNetworkConfig{Enabled: true, Headscale: config.HeadscaleConfig{
		URL: server.URL, APIKeyEnv: "KEY", ServiceTag: config.DefaultEgressServiceTag,
	}}}
	service, err := NewService(cfg, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	service.headscale.lookupEnv = func(string) string { return "test-key" }

	nodes, err := service.SyncNodes(context.Background())
	if err != nil {
		t.Fatalf("SyncNodes() error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "17" {
		t.Fatalf("SyncNodes() = %#v", nodes)
	}

	serverResponse = `{"nodes":[]}`
	if _, err = service.SyncNodes(context.Background()); err != nil {
		t.Fatalf("second SyncNodes() error = %v", err)
	}
	node, err := service.store.GetNode(context.Background(), "17")
	if err != nil {
		t.Fatalf("GetNode() error = %v", err)
	}
	if node.Online {
		t.Fatal("removed node should be offline")
	}
}

func newTestService(t *testing.T, enabled bool) *Service {
	t.Helper()
	cfg := &config.Config{EgressNetwork: config.EgressNetworkConfig{Enabled: enabled, Headscale: config.HeadscaleConfig{
		ServiceTag: config.DefaultEgressServiceTag,
	}}}
	service, err := NewService(cfg, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}
