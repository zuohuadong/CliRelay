package main

import (
	"os"
	"strings"
	"testing"
)

func TestAuthFilesQuotaAssetSupportsAnthropicOAuthUsage(t *testing.T) {
	data, err := os.ReadFile("assets/AuthFilesPage-8ofG866A.js")
	if err != nil {
		t.Fatalf("read auth files asset: %v", err)
	}
	content := string(data)

	for _, want := range []string{
		`https://api.anthropic.com/api/oauth/usage`,
		`oauth-2025-04-20`,
		`five_hour`,
		`seven_day`,
		`seven_day_sonnet`,
		`a==="anthropic"||a==="claude"?"anthropic"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("auth files quota asset missing Anthropic OAuth usage support marker %q", want)
		}
	}
}
