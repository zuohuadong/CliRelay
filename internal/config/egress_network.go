package config

import (
	"net/url"
	"strings"
	"time"
)

var DefaultEgressProbeURLs = []string{
	"https://api.ipify.org?format=json",
	"https://ifconfig.co/json",
}

const (
	DefaultEgressServiceTag      = "tag:clirelay-egress"
	DefaultEgressEnrollmentTTL   = "1h"
	DefaultHeadscaleAPIKeyEnv    = "HEADSCALE_API_KEY"
	DefaultEgressBindingPolicy   = "exclusive"
	DefaultEndpointCheckInterval = "2m"
	DefaultEndpointHealthTTL     = "5m"
	DefaultHeadscaleSyncInterval = "1m"
	DefaultNodeFreshnessTTL      = "3m"
)

type EgressNetworkConfig struct {
	Enabled               bool            `yaml:"enabled" json:"enabled"`
	LocalEndpointEnabled  bool            `yaml:"local-endpoint-enabled" json:"local-endpoint-enabled"`
	BindingPolicy         string          `yaml:"binding-policy" json:"binding-policy"`
	EndpointCheckInterval string          `yaml:"endpoint-check-interval" json:"endpoint-check-interval"`
	EndpointHealthTTL     string          `yaml:"endpoint-health-ttl" json:"endpoint-health-ttl"`
	ProbeURLs             []string        `yaml:"probe-urls" json:"probe-urls"`
	Headscale             HeadscaleConfig `yaml:"headscale" json:"headscale"`
}

type HeadscaleConfig struct {
	URL              string `yaml:"url" json:"url"`
	APIKeyEnv        string `yaml:"api-key-env" json:"api-key-env"`
	ServiceTag       string `yaml:"service-tag" json:"service-tag"`
	EnrollmentTTL    string `yaml:"enrollment-ttl" json:"enrollment-ttl"`
	SyncInterval     string `yaml:"sync-interval" json:"sync-interval"`
	NodeFreshnessTTL string `yaml:"node-freshness-ttl" json:"node-freshness-ttl"`
}

func (cfg *Config) SanitizeEgressNetwork() {
	if cfg == nil {
		return
	}
	headscale := &cfg.EgressNetwork.Headscale
	headscale.URL = strings.TrimRight(strings.TrimSpace(headscale.URL), "/")
	headscale.APIKeyEnv = strings.TrimSpace(headscale.APIKeyEnv)
	if headscale.APIKeyEnv == "" {
		headscale.APIKeyEnv = DefaultHeadscaleAPIKeyEnv
	}
	headscale.ServiceTag = strings.TrimSpace(headscale.ServiceTag)
	if headscale.ServiceTag == "" {
		headscale.ServiceTag = DefaultEgressServiceTag
	}
	if !strings.HasPrefix(headscale.ServiceTag, "tag:") {
		headscale.ServiceTag = "tag:" + headscale.ServiceTag
	}
	headscale.EnrollmentTTL = strings.TrimSpace(headscale.EnrollmentTTL)
	if duration, err := time.ParseDuration(headscale.EnrollmentTTL); err != nil || duration <= 0 {
		headscale.EnrollmentTTL = DefaultEgressEnrollmentTTL
	}
	cfg.EgressNetwork.BindingPolicy = strings.ToLower(strings.TrimSpace(cfg.EgressNetwork.BindingPolicy))
	if cfg.EgressNetwork.BindingPolicy != DefaultEgressBindingPolicy {
		cfg.EgressNetwork.BindingPolicy = DefaultEgressBindingPolicy
	}
	checkText, checkInterval := sanitizePositiveDuration(cfg.EgressNetwork.EndpointCheckInterval, DefaultEndpointCheckInterval)
	healthText, _ := sanitizeTTL(cfg.EgressNetwork.EndpointHealthTTL, DefaultEndpointHealthTTL, checkInterval)
	syncText, syncInterval := sanitizePositiveDuration(headscale.SyncInterval, DefaultHeadscaleSyncInterval)
	freshnessText, _ := sanitizeTTL(headscale.NodeFreshnessTTL, DefaultNodeFreshnessTTL, syncInterval)
	cfg.EgressNetwork.EndpointCheckInterval = checkText
	cfg.EgressNetwork.EndpointHealthTTL = healthText
	cfg.EgressNetwork.ProbeURLs = sanitizeEgressProbeURLs(cfg.EgressNetwork.ProbeURLs)
	headscale.SyncInterval = syncText
	headscale.NodeFreshnessTTL = freshnessText
}

func sanitizeEgressProbeURLs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		parsed, err := url.Parse(value)
		if err != nil || !parsed.IsAbs() || !strings.EqualFold(parsed.Scheme, "https") || strings.TrimSpace(parsed.Host) == "" || parsed.User != nil {
			continue
		}
		parsed.Scheme = "https"
		value = parsed.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultEgressProbeURLs...)
	}
	return out
}

func sanitizePositiveDuration(value, fallback string) (string, time.Duration) {
	value = strings.TrimSpace(value)
	duration, err := time.ParseDuration(value)
	if err == nil && duration > 0 {
		return value, duration
	}
	duration, _ = time.ParseDuration(fallback)
	return fallback, duration
}

func sanitizeTTL(value, fallback string, interval time.Duration) (string, time.Duration) {
	text, ttl := sanitizePositiveDuration(value, fallback)
	if ttl <= interval {
		ttl = interval * 2
		text = ttl.String()
	}
	return text, ttl
}
