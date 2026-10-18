package auth

import (
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestFindAllAntigravityCreditsCandidateAuths_PrefersKnownCreditsThenUnknown(t *testing.T) {
	m := &Manager{
		auths: map[string]*Auth{
			"zz-credits": {ID: "zz-credits", Provider: "antigravity"},
			"aa-unknown": {ID: "aa-unknown", Provider: "antigravity"},
			"mm-no":      {ID: "mm-no", Provider: "antigravity"},
		},
		executors: map[string]ProviderExecutor{
			"antigravity": schedulerTestExecutor{},
		},
	}

	SetAntigravityCreditsHint("zz-credits", AntigravityCreditsHint{
		Known:     true,
		Available: true,
		UpdatedAt: time.Now(),
	})
	SetAntigravityCreditsHint("mm-no", AntigravityCreditsHint{
		Known:     true,
		Available: false,
		UpdatedAt: time.Now(),
	})

	opts := cliproxyexecutor.Options{}

	candidates := m.findAllAntigravityCreditsCandidateAuths("claude-sonnet-4-6", opts)
	if len(candidates) != 2 {
		t.Fatalf("candidates len = %d, want 2", len(candidates))
	}
	if candidates[0].auth.ID != "zz-credits" {
		t.Fatalf("candidates[0].auth.ID = %q, want %q", candidates[0].auth.ID, "zz-credits")
	}
	if candidates[1].auth.ID != "aa-unknown" {
		t.Fatalf("candidates[1].auth.ID = %q, want %q", candidates[1].auth.ID, "aa-unknown")
	}

	nonClaude := m.findAllAntigravityCreditsCandidateAuths("gemini-3-flash", opts)
	if len(nonClaude) != 0 {
		t.Fatalf("nonClaude len = %d, want 0", len(nonClaude))
	}

	pinnedOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "aa-unknown"},
	}
	pinned := m.findAllAntigravityCreditsCandidateAuths("claude-sonnet-4-6", pinnedOpts)
	if len(pinned) != 1 {
		t.Fatalf("pinned len = %d, want 1", len(pinned))
	}
	if pinned[0].auth.ID != "aa-unknown" {
		t.Fatalf("pinned[0].auth.ID = %q, want %q", pinned[0].auth.ID, "aa-unknown")
	}

	// Once the negative hint expires (>= 10 minutes), mm-no should be retried as an unknown candidate.
	SetAntigravityCreditsHint("mm-no", AntigravityCreditsHint{
		Known:     true,
		Available: false,
		UpdatedAt: time.Now().Add(-10*time.Minute - time.Second),
	})
	t.Cleanup(func() {
		SetAntigravityCreditsHint("zz-credits", AntigravityCreditsHint{})
		SetAntigravityCreditsHint("mm-no", AntigravityCreditsHint{})
	})

	afterExpiry, errAfterExpiry := m.findAllAntigravityCreditsCandidateAuths(context.Background(), "claude-sonnet-4-6", opts)
	if errAfterExpiry != nil {
		t.Fatalf("findAllAntigravityCreditsCandidateAuths(after expiry) error = %v", errAfterExpiry)
	}
	if len(afterExpiry) != 3 {
		t.Fatalf("afterExpiry candidates len = %d, want 3", len(afterExpiry))
	}
	if afterExpiry[0].auth.ID != "zz-credits" || afterExpiry[1].auth.ID != "aa-unknown" || afterExpiry[2].auth.ID != "mm-no" {
		t.Fatalf("afterExpiry order = [%s, %s, %s], want [zz-credits, aa-unknown, mm-no]",
			afterExpiry[0].auth.ID, afterExpiry[1].auth.ID, afterExpiry[2].auth.ID)
	}
}
