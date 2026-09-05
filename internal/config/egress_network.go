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
	DefaultEndpointCheckInterval = "2m"
	DefaultEndpointHealthTTL     = "5m"
)

type EgressNetworkConfig struct {
	Enabled               bool     `yaml:"enabled" json:"enabled"`
	EndpointCheckInterval string   `yaml:"endpoint-check-interval" json:"endpoint-check-interval"`
	EndpointHealthTTL     string   `yaml:"endpoint-health-ttl" json:"endpoint-health-ttl"`
	ProbeURLs             []string `yaml:"probe-urls" json:"probe-urls"`
	// UpstreamProbeURLs are optional reachability probes for upstream origins
	// (e.g. https://chatgpt.com/backend-api/models). Unlike probe-urls they do
	// not return the public IP; they only need to prove the endpoint can reach
	// the origin without being blocked by edge nodes (403/404/5xx). Empty by
	// default, which keeps legacy IP-only health checks unchanged.
	UpstreamProbeURLs []string `yaml:"upstream-probe-urls" json:"upstream-probe-urls"`
}

func (cfg *Config) SanitizeEgressNetwork() {
	if cfg == nil {
		return
	}
	checkText, checkInterval := sanitizePositiveDuration(cfg.EgressNetwork.EndpointCheckInterval, DefaultEndpointCheckInterval)
	healthText, _ := sanitizeTTL(cfg.EgressNetwork.EndpointHealthTTL, DefaultEndpointHealthTTL, checkInterval)
	cfg.EgressNetwork.EndpointCheckInterval = checkText
	cfg.EgressNetwork.EndpointHealthTTL = healthText
	cfg.EgressNetwork.ProbeURLs = sanitizeEgressProbeURLs(cfg.EgressNetwork.ProbeURLs)
	cfg.EgressNetwork.UpstreamProbeURLs = sanitizeHTTPSProbeURLs(cfg.EgressNetwork.UpstreamProbeURLs)
}

func sanitizeEgressProbeURLs(values []string) []string {
	out := sanitizeHTTPSProbeURLs(values)
	if len(out) == 0 {
		return append([]string(nil), DefaultEgressProbeURLs...)
	}
	return out
}

// sanitizeHTTPSProbeURLs normalizes https probe URLs without applying any
// default fallback, so empty input stays empty (feature disabled).
func sanitizeHTTPSProbeURLs(values []string) []string {
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
