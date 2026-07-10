package config

import (
	"testing"
	"time"
)

func TestParseConfigBytesSanitizesEgressNetwork(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfigBytes([]byte(`
egress-network:
  enabled: true
  allow-local-server: false
  headscale:
    url: " https://headscale.example.com/ "
    api-key-env: " HEADSCALE_API_KEY "
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.EgressNetwork.Enabled {
		t.Fatal("egress network should be enabled")
	}
	if cfg.EgressNetwork.Headscale.URL != "https://headscale.example.com" {
		t.Fatalf("headscale url = %q", cfg.EgressNetwork.Headscale.URL)
	}
	if cfg.EgressNetwork.Headscale.APIKeyEnv != "HEADSCALE_API_KEY" {
		t.Fatalf("api key env = %q", cfg.EgressNetwork.Headscale.APIKeyEnv)
	}
	if cfg.EgressNetwork.Headscale.ServiceTag != DefaultEgressServiceTag {
		t.Fatalf("service tag = %q, want %q", cfg.EgressNetwork.Headscale.ServiceTag, DefaultEgressServiceTag)
	}
	if cfg.EgressNetwork.Headscale.EnrollmentTTL != DefaultEgressEnrollmentTTL {
		t.Fatalf("enrollment ttl = %q, want %q", cfg.EgressNetwork.Headscale.EnrollmentTTL, DefaultEgressEnrollmentTTL)
	}
}

func TestSanitizeEgressNetworkHardeningDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.SanitizeEgressNetwork()
	if cfg.EgressNetwork.BindingPolicy != DefaultEgressBindingPolicy {
		t.Fatalf("binding policy = %q", cfg.EgressNetwork.BindingPolicy)
	}
	if cfg.EgressNetwork.EndpointCheckInterval != DefaultEndpointCheckInterval {
		t.Fatalf("endpoint check interval = %q", cfg.EgressNetwork.EndpointCheckInterval)
	}
	if cfg.EgressNetwork.EndpointHealthTTL != DefaultEndpointHealthTTL {
		t.Fatalf("endpoint health ttl = %q", cfg.EgressNetwork.EndpointHealthTTL)
	}
	if cfg.EgressNetwork.Headscale.SyncInterval != DefaultHeadscaleSyncInterval {
		t.Fatalf("headscale sync interval = %q", cfg.EgressNetwork.Headscale.SyncInterval)
	}
	if cfg.EgressNetwork.Headscale.NodeFreshnessTTL != DefaultNodeFreshnessTTL {
		t.Fatalf("node freshness ttl = %q", cfg.EgressNetwork.Headscale.NodeFreshnessTTL)
	}
}

func TestSanitizeEgressNetworkMakesTTLsLongerThanIntervals(t *testing.T) {
	t.Parallel()

	cfg := &Config{EgressNetwork: EgressNetworkConfig{
		BindingPolicy:         "shared",
		EndpointCheckInterval: "10m",
		EndpointHealthTTL:     "1m",
		Headscale: HeadscaleConfig{
			SyncInterval:     "20m",
			NodeFreshnessTTL: "2m",
		},
	}}
	cfg.SanitizeEgressNetwork()
	if cfg.EgressNetwork.BindingPolicy != DefaultEgressBindingPolicy {
		t.Fatalf("unsupported binding policy = %q", cfg.EgressNetwork.BindingPolicy)
	}
	check, _ := time.ParseDuration(cfg.EgressNetwork.EndpointCheckInterval)
	health, _ := time.ParseDuration(cfg.EgressNetwork.EndpointHealthTTL)
	syncInterval, _ := time.ParseDuration(cfg.EgressNetwork.Headscale.SyncInterval)
	freshness, _ := time.ParseDuration(cfg.EgressNetwork.Headscale.NodeFreshnessTTL)
	if health <= check {
		t.Fatalf("endpoint health ttl %s must be > interval %s", health, check)
	}
	if freshness <= syncInterval {
		t.Fatalf("node freshness ttl %s must be > interval %s", freshness, syncInterval)
	}
}

func TestSanitizeEgressProbeURLsRequiresCredentialFreeHTTPSAndDeduplicates(t *testing.T) {
	t.Parallel()
	cfg := &Config{EgressNetwork: EgressNetworkConfig{ProbeURLs: []string{
		" https://probe.example/ip ",
		"https://probe.example/ip",
		"http://insecure.example/ip",
		"https://user:pass@secret.example/ip",
		"not-a-url",
	}}}
	cfg.SanitizeEgressNetwork()
	if len(cfg.EgressNetwork.ProbeURLs) != 1 || cfg.EgressNetwork.ProbeURLs[0] != "https://probe.example/ip" {
		t.Fatalf("probe URLs = %#v", cfg.EgressNetwork.ProbeURLs)
	}
	cfg.EgressNetwork.ProbeURLs = nil
	cfg.SanitizeEgressNetwork()
	if len(cfg.EgressNetwork.ProbeURLs) < 2 {
		t.Fatalf("default probe URLs = %#v", cfg.EgressNetwork.ProbeURLs)
	}
}
