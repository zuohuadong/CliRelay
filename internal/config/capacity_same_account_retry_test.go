package config

import "testing"

func TestParseCapacitySameAccountRetries(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
capacity-same-account-retries: 2
codex:
  capacity-same-account-retries: 0
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	if cfg.CapacitySameAccountRetries == nil || *cfg.CapacitySameAccountRetries != 2 {
		t.Fatalf("global capacity retries = %v, want 2", cfg.CapacitySameAccountRetries)
	}
	if cfg.Codex.CapacitySameAccountRetries == nil || *cfg.Codex.CapacitySameAccountRetries != 0 {
		t.Fatalf("codex capacity retries = %v, want explicit 0", cfg.Codex.CapacitySameAccountRetries)
	}
}

func TestParseCapacitySameAccountRetriesLeavesUnsetNil(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	if cfg.CapacitySameAccountRetries != nil || cfg.Codex.CapacitySameAccountRetries != nil {
		t.Fatalf("unset capacity retries must remain nil: global=%v codex=%v", cfg.CapacitySameAccountRetries, cfg.Codex.CapacitySameAccountRetries)
	}
}
