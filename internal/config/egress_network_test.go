package config

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestEgressNetworkConfigHasOnlySimpleControlPlaneFields(t *testing.T) {
	t.Parallel()

	typeOfConfig := reflect.TypeOf(EgressNetworkConfig{})
	fields := make([]string, 0, typeOfConfig.NumField())
	for i := 0; i < typeOfConfig.NumField(); i++ {
		fields = append(fields, typeOfConfig.Field(i).Tag.Get("yaml"))
	}
	sort.Strings(fields)
	want := []string{"enabled", "endpoint-check-interval", "endpoint-health-ttl", "probe-urls"}
	sort.Strings(want)
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("EgressNetworkConfig yaml fields = %#v, want %#v", fields, want)
	}
}

func TestParseConfigBytesSanitizesSimpleEgressNetwork(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfigBytes([]byte(`
egress-network:
  enabled: true
  endpoint-check-interval: 10m
  endpoint-health-ttl: 1m
  probe-urls:
    - " https://probe.example/ip "
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.EgressNetwork.Enabled {
		t.Fatal("egress network should be enabled")
	}
	check, _ := time.ParseDuration(cfg.EgressNetwork.EndpointCheckInterval)
	health, _ := time.ParseDuration(cfg.EgressNetwork.EndpointHealthTTL)
	if health <= check {
		t.Fatalf("endpoint health ttl %s must be > interval %s", health, check)
	}
	if got := cfg.EgressNetwork.ProbeURLs; len(got) != 1 || got[0] != "https://probe.example/ip" {
		t.Fatalf("probe URLs = %#v", got)
	}
}

func TestSanitizeEgressNetworkDefaultsAndFiltersProbeURLs(t *testing.T) {
	t.Parallel()

	cfg := &Config{EgressNetwork: EgressNetworkConfig{ProbeURLs: []string{
		"https://probe.example/ip",
		"https://probe.example/ip",
		"http://insecure.example/ip",
		"https://user:pass@secret.example/ip",
		"not-a-url",
	}}}
	cfg.SanitizeEgressNetwork()
	if cfg.EgressNetwork.EndpointCheckInterval != DefaultEndpointCheckInterval {
		t.Fatalf("endpoint check interval = %q", cfg.EgressNetwork.EndpointCheckInterval)
	}
	if cfg.EgressNetwork.EndpointHealthTTL != DefaultEndpointHealthTTL {
		t.Fatalf("endpoint health ttl = %q", cfg.EgressNetwork.EndpointHealthTTL)
	}
	if got := cfg.EgressNetwork.ProbeURLs; len(got) != 1 || got[0] != "https://probe.example/ip" {
		t.Fatalf("probe URLs = %#v", got)
	}

	cfg.EgressNetwork.ProbeURLs = nil
	cfg.SanitizeEgressNetwork()
	if len(cfg.EgressNetwork.ProbeURLs) < 2 {
		t.Fatalf("default probe URLs = %#v", cfg.EgressNetwork.ProbeURLs)
	}
}
