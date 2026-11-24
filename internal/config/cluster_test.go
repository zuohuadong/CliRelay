package config

import "testing"

func TestClusterEnvOverrides(t *testing.T) {
	t.Setenv(EnvClusterEnabled, "true")
	t.Setenv(EnvClusterNodeID, "n2")
	cfg := &Config{}
	cfg.ApplyEnvOverrides()
	if !cfg.Cluster.Enabled || cfg.Cluster.NodeID != "n2" {
		t.Fatalf("cluster env overrides not applied: %+v", cfg.Cluster)
	}
}

func TestClusterDisabledByDefault(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyEnvOverrides()
	if cfg.Cluster.Enabled {
		t.Fatalf("cluster mode must be opt-in")
	}
}
