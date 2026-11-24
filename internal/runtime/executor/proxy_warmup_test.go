package executor

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestProxyWarmManagerAllowsExactAndSubdomainSuffixesOnly(t *testing.T) {
	t.Parallel()

	manager := NewProxyWarmManager(config.ProxyWarmConfig{
		Enabled:             true,
		AllowedHostSuffixes: []string{"openai.com"},
	}, "http://127.0.0.1:8080", nil)

	if !manager.isHostAllowed("openai.com") {
		t.Fatal("openai.com should be allowed")
	}
	if !manager.isHostAllowed("api.openai.com") {
		t.Fatal("api.openai.com should be allowed")
	}
	if manager.isHostAllowed("badopenai.com") {
		t.Fatal("badopenai.com should not match openai.com suffix")
	}
}

func TestNormalizeWarmHostRemovesURLAndPort(t *testing.T) {
	t.Parallel()

	if got := normalizeWarmHost("https://API.OpenAI.com:443/v1/responses"); got != "api.openai.com" {
		t.Fatalf("normalizeWarmHost URL = %q, want api.openai.com", got)
	}
	if got := normalizeWarmHost("[2001:db8::1]:443"); got != "2001:db8::1" {
		t.Fatalf("normalizeWarmHost IPv6 = %q, want 2001:db8::1", got)
	}
}

func TestProxyWarmManagerSeedsConfiguredTargets(t *testing.T) {
	t.Parallel()

	manager := NewProxyWarmManager(config.ProxyWarmConfig{
		Enabled:             true,
		AllowedHostSuffixes: []string{"openai.com"},
		Targets: []config.ProxyWarmTarget{
			{Host: "api.openai.com", URL: "https://api.openai.com/", Method: ""},
			{Host: "badopenai.com", URL: "https://badopenai.com/", Method: "GET"},
		},
	}, "http://127.0.0.1:8080", nil)

	if _, ok := manager.hosts["api.openai.com"]; !ok {
		t.Fatal("configured api.openai.com target was not seeded")
	}
	if manager.hosts["api.openai.com"].targetMethod != http.MethodHead {
		t.Fatalf("target method = %q, want HEAD", manager.hosts["api.openai.com"].targetMethod)
	}
	if _, ok := manager.hosts["badopenai.com"]; ok {
		t.Fatal("lookalike suffix host should not be seeded")
	}
}

func TestProxyWarmManagerDrainsWarmupResponseForConnectionReuse(t *testing.T) {
	var newConnections atomic.Int32
	proxyServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("warm body"))
	}))
	proxyServer.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	proxyServer.Start()
	defer proxyServer.Close()

	manager := NewProxyWarmManager(config.ProxyWarmConfig{
		Enabled:               true,
		IntervalSeconds:       60,
		IntervalJitterSeconds: 0,
		TimeoutSeconds:        1,
		AllowedHostSuffixes:   []string{"target.test"},
		Targets: []config.ProxyWarmTarget{
			{Host: "target.test", URL: "http://target.test/warm", Method: http.MethodGet},
		},
	}, proxyServer.URL, nil)
	manager.ctx = context.Background()
	state := &warmHostState{
		host:         "target.test",
		targetURL:    "http://target.test/warm",
		targetMethod: http.MethodGet,
		lastUsed:     time.Now(),
	}

	manager.doWarm("target.test", state, time.Second)
	manager.doWarm("target.test", state, time.Second)

	if got := newConnections.Load(); got > 1 {
		t.Fatalf("warmup opened %d proxy connections, want reuse of the first connection", got)
	}
	if transport := cachedProxyTransport(proxyServer.URL, nil); transport != nil {
		transport.CloseIdleConnections()
	}
}

func TestProxyWarmManagerRunsFirstWarmRoundAfterStartupDelay(t *testing.T) {
	warmed := make(chan struct{}, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case warmed <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxyServer.Close()

	manager := NewProxyWarmManager(config.ProxyWarmConfig{
		Enabled:               true,
		StartupDelaySeconds:   0,
		IntervalSeconds:       60,
		IntervalJitterSeconds: 0,
		TimeoutSeconds:        1,
		AllowedHostSuffixes:   []string{"target.test"},
		Targets: []config.ProxyWarmTarget{
			{Host: "target.test", URL: "http://target.test/warm", Method: http.MethodHead},
		},
	}, proxyServer.URL, nil)
	manager.Start(context.Background())
	defer manager.Stop()

	select {
	case <-warmed:
	case <-time.After(time.Second):
		t.Fatal("warmup did not run immediately after startup delay")
	}
}
