package config

import "testing"

func TestSanitizeRequestPoliciesKeepsAutoContextCompressionAndAppliesSafeDefaults(t *testing.T) {
	cfg := &Config{RequestPolicies: []RequestPolicy{{
		Name: " auto-compress ",
		OverLimit: RequestPolicyOverLimit{
			Action: "COMPRESS",
			Compression: RequestPolicyCompression{
				Provider: " GEMINI ",
				Model:    " gemini-flash ",
			},
		},
	}}}
	cfg.SanitizeRequestPolicies()
	if len(cfg.RequestPolicies) != 1 {
		t.Fatalf("request policies = %d, want 1", len(cfg.RequestPolicies))
	}
	policy := cfg.RequestPolicies[0]
	compression := policy.OverLimit.Compression
	if policy.Name != "auto-compress" || policy.OverLimit.Action != "compress" {
		t.Fatalf("policy = %#v", policy)
	}
	if compression.Provider != "gemini" || compression.Model != "gemini-flash" {
		t.Fatalf("compressor route = %q/%q", compression.Provider, compression.Model)
	}
	if compression.UnavailableAction != "reject" || compression.TriggerRatio != 0.82 || compression.TargetRatio != 0.60 {
		t.Fatalf("safe defaults were not applied: %#v", compression)
	}
	if compression.PreserveRecentItems != 8 || compression.CacheTTLSeconds != 3600 || compression.CacheMaxEntries != 4096 || compression.MediaMode != "auto" {
		t.Fatalf("relay defaults were not applied: %#v", compression)
	}
}

func TestSanitizeRequestPoliciesDropsLimitlessCompressionWhenAutoContextDisabled(t *testing.T) {
	disabled := false
	cfg := &Config{RequestPolicies: []RequestPolicy{{
		Name: "inactive",
		OverLimit: RequestPolicyOverLimit{
			Action: "compress",
			Compression: RequestPolicyCompression{
				Provider:    "gemini",
				Model:       "gemini-flash",
				AutoContext: &disabled,
			},
		},
	}}}
	cfg.SanitizeRequestPolicies()
	if len(cfg.RequestPolicies) != 0 {
		t.Fatalf("request policies = %#v, want none", cfg.RequestPolicies)
	}
}
