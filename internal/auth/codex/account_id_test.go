package codex

import "testing"

func TestAccountIDFromMetadata(t *testing.T) {
	idToken := "eyJhbGciOiAiUlMyNTYiLCAidHlwIjogIkpXVCJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOiB7ImNoYXRncHRfYWNjb3VudF9pZCI6ICJhY2N0LWZyb20tand0In19.ZmFrZXNpZw"

	if got := AccountIDFromMetadata(nil); got != "" {
		t.Fatalf("nil metadata = %q, want empty", got)
	}
	if got := AccountIDFromMetadata(map[string]any{}); got != "" {
		t.Fatalf("empty metadata = %q, want empty", got)
	}
	// Explicit account_id wins.
	if got := AccountIDFromMetadata(map[string]any{"account_id": "acct-direct"}); got != "acct-direct" {
		t.Fatalf("explicit account_id = %q, want acct-direct", got)
	}
	// Whitespace-only account_id falls through to JWT.
	m := map[string]any{"account_id": "  ", "id_token": idToken}
	if got := AccountIDFromMetadata(m); got != "acct-from-jwt" {
		t.Fatalf("whitespace account_id = %q, want acct-from-jwt", got)
	}
	if v, _ := m["account_id"].(string); v != "acct-from-jwt" {
		t.Fatalf("metadata not backfilled: %v", m["account_id"])
	}
	// No id_token and no account_id.
	if got := AccountIDFromMetadata(map[string]any{"email": "x"}); got != "" {
		t.Fatalf("no identity metadata = %q, want empty", got)
	}
}
