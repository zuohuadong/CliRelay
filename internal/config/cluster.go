package config

import (
	"os"
	"strconv"
	"strings"
)

// ClusterConfig controls multi-instance deployment: several CliRelay
// processes sharing one PostgreSQL primary. Leaving it disabled keeps the
// single-node behaviour unchanged.
type ClusterConfig struct {
	// Enabled turns on cluster coordination: credentials and settings are
	// shared through PostgreSQL, periodic maintenance runs on one elected
	// node, and changes are broadcast to the other nodes.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// NodeID uniquely names this process among the nodes sharing a
	// database. Empty means the host name.
	NodeID string `yaml:"node-id,omitempty" json:"node-id,omitempty"`
	// Advertise is an optional address shown in the cluster status view,
	// for operators only.
	Advertise string `yaml:"advertise,omitempty" json:"advertise,omitempty"`
}

// Environment overrides for the cluster block, so secrets and per-node
// values can live in the systemd EnvironmentFile instead of config.yaml.
const (
	EnvClusterEnabled   = "CLIRELAY_CLUSTER_ENABLED"
	EnvClusterNodeID    = "CLIRELAY_CLUSTER_NODE_ID"
	EnvClusterAdvertise = "CLIRELAY_CLUSTER_ADVERTISE"
)

func (cfg *Config) applyClusterEnvOverrides() {
	if raw := strings.TrimSpace(os.Getenv(EnvClusterEnabled)); raw != "" {
		if enabled, err := strconv.ParseBool(raw); err == nil {
			cfg.Cluster.Enabled = enabled
		}
	}
	setString := func(dst *string, key string) {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			*dst = v
		}
	}
	setString(&cfg.Cluster.NodeID, EnvClusterNodeID)
	setString(&cfg.Cluster.Advertise, EnvClusterAdvertise)
}
