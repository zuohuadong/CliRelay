package management

import (
	"testing"
	"time"
)

func TestCompleteOAuthSessionRetainsCompletedState(t *testing.T) {
	previousStore := oauthSessions
	oauthSessions = newOAuthSessionStore(time.Minute)
	t.Cleanup(func() {
		oauthSessions = previousStore
	})

	const (
		completedState = "completed-state"
		staleState     = "stale-state"
	)

	RegisterOAuthSession(completedState, "codex")
	RegisterOAuthSession(staleState, "codex")

	CompleteOAuthSession(completedState)
	removed := CompleteOAuthSessionsByProvider("codex")
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	provider, status, ok := GetOAuthSession(completedState)
	if !ok {
		t.Fatal("expected completed session to remain queryable")
	}
	if provider != "codex" {
		t.Fatalf("provider = %q, want codex", provider)
	}
	if status != oauthSessionStatusCompleted {
		t.Fatalf("status = %q, want %q", status, oauthSessionStatusCompleted)
	}

	if _, _, ok := GetOAuthSession(staleState); ok {
		t.Fatal("expected stale pending session to be removed")
	}
}
