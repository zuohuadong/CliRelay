package config

import (
	"crypto/sha256"
	"encoding/hex"

	"gopkg.in/yaml.v3"
)

// ComputeSourceHash returns the stable ETag digest used by management config
// endpoints.
func ComputeSourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ParseConfigBytes parses a configuration document without touching disk.
func ParseConfigBytes(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	cfg.SanitizeOpenAICompatibility()
	cfg.SanitizeOAuthModelAlias()
	return &cfg, nil
}

// NormalizeConfigLayout and ValidateV8Config are implemented in config_v8.go.
