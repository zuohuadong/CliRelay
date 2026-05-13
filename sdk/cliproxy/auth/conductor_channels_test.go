package auth

import "testing"

func TestAllowedChannelsFromMetadataParsesStringList(t *testing.T) {
	t.Parallel()

	allowed := allowedChannelsFromMetadata(map[string]any{
		"allowed-channels": " Team Alpha,team beta,,TEAM ALPHA ",
	})

	if len(allowed) != 2 {
		t.Fatalf("allowed channel count = %d, want 2", len(allowed))
	}
	if _, ok := allowed["team alpha"]; !ok {
		t.Fatalf("expected normalized channel %q", "team alpha")
	}
	if _, ok := allowed["team beta"]; !ok {
		t.Fatalf("expected normalized channel %q", "team beta")
	}
}

func TestAuthAllowedByChannelsUsesResolvedChannelName(t *testing.T) {
	t.Parallel()

	allowed := map[string]struct{}{
		"team alpha": {},
	}

	if !authAllowedByChannels(&Auth{Label: "Team Alpha"}, allowed) {
		t.Fatal("expected explicit auth label to match allowed channels")
	}
	if !authAllowedByChannels(&Auth{
		Provider: "claude",
		Metadata: map[string]any{
			"label": "Team Alpha",
		},
	}, allowed) {
		t.Fatal("expected metadata label to match allowed channels")
	}
	if !authAllowedByChannels(&Auth{
		Label: "chatgpt-pro1",
		Metadata: map[string]any{
			"email": "team-alpha@example.com",
		},
	}, map[string]struct{}{"team-alpha@example.com": {}}) {
		t.Fatal("expected legacy email alias to match renamed channel")
	}
	if !authAllowedByChannels(&Auth{
		Provider: "claude",
		Metadata: map[string]any{
			"email": "team-alpha@example.com",
		},
	}, map[string]struct{}{"team-alpha@example.com": {}}) {
		t.Fatal("expected email fallback to match allowed channels")
	}
	if authAllowedByChannels(&Auth{Provider: "claude"}, allowed) {
		t.Fatal("expected unmatched provider fallback to be rejected")
	}
}
