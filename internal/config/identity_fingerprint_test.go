package config

import "testing"

func TestDefaultCodexIdentityFingerprintUsesCurrentVersionAndDynamicSessions(t *testing.T) {
	t.Parallel()

	got := DefaultCodexIdentityFingerprint()

	if got.Enabled {
		t.Fatalf("Enabled = true, want false by default")
	}
	if got.Version != "" {
		t.Fatalf("Version = %q, want empty (codex-tui does not require Version)", got.Version)
	}
	if got.UserAgent != "codex-tui/0.118.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9 (codex-tui; 0.118.0)" {
		t.Fatalf("UserAgent = %q, want codex-tui user agent", got.UserAgent)
	}
	if got.SessionMode != "per-request" {
		t.Fatalf("SessionMode = %q, want per-request", got.SessionMode)
	}
}

func TestNormalizeCodexIdentityFingerprintAppliesCurrentDefaults(t *testing.T) {
	t.Parallel()

	got := NormalizeCodexIdentityFingerprint(CodexIdentityFingerprintConfig{})

	if got.Version != "" {
		t.Fatalf("Version = %q, want empty (codex-tui does not require Version)", got.Version)
	}
	if got.UserAgent != "codex-tui/0.118.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9 (codex-tui; 0.118.0)" {
		t.Fatalf("UserAgent = %q, want codex-tui user agent", got.UserAgent)
	}
	if got.SessionMode != "per-request" {
		t.Fatalf("SessionMode = %q, want per-request", got.SessionMode)
	}
}
