package registry

import "testing"

func TestStripContextWindowMarker(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"claude-sonnet-4-20250514 [128K]", "claude-sonnet-4-20250514"},
		{"gpt-4o [1M]", "gpt-4o"},
		{"gpt-4o-mini [200K]", "gpt-4o-mini"},
		{"claude-opus-4 [2M]", "claude-opus-4"},
		{"model-no-marker", "model-no-marker"},
		{"model-with-brackets-not-at-end [128K] suffix", "model-with-brackets-not-at-end [128K] suffix"},
		{"", ""},
		{"  spaced  [128K]  ", "  spaced"},
		{"model[128K]", "model[128K]"}, // no space before bracket, not a marker
	}

	for _, tt := range tests {
		got := StripContextWindowMarker(tt.input)
		if got != tt.expected {
			t.Errorf("StripContextWindowMarker(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestNormalizeModelID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"claude-sonnet-4-20250514 [128K]", "claude-sonnet-4-20250514"},
		{"  gpt-4o [1M]  ", "  gpt-4o"},
		{"model-no-marker", "model-no-marker"},
	}

	for _, tt := range tests {
		got := NormalizeModelID(tt.input)
		if got != tt.expected {
			t.Errorf("NormalizeModelID(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
