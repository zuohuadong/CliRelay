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

func TestAuthFilesQuotaColumnHasInlineRefreshAction(t *testing.T) {
	data, err := os.ReadFile("assets/AuthFilesPage-8ofG866A.js")
	if err != nil {
		t.Fatalf("read auth files asset: %v", err)
	}
	content := string(data)
	quotaIdx := strings.Index(content, `key:"quota"`)
	if quotaIdx < 0 {
		t.Fatal("auth files asset missing quota column")
	}
	enabledIdx := strings.Index(content[quotaIdx:], `key:"enabled"`)
	if enabledIdx < 0 {
		t.Fatal("auth files asset missing enabled column after quota column")
	}
	quotaColumn := content[quotaIdx : quotaIdx+enabledIdx]
	if !strings.Contains(quotaColumn, `onClick:()=>{We(s,i)}`) {
		t.Fatal("quota column should expose an inline refresh action")
	}
}

func TestAuthFilesQuotaAssetSupportsCurrentAntigravityModelCatalog(t *testing.T) {
	data, err := os.ReadFile("assets/AuthFilesPage-8ofG866A.js")
	if err != nil {
		t.Fatalf("read auth files asset: %v", err)
	}
	content := string(data)

	for _, want := range []string{
		`gemini-3.1-pro-high`,
		`gemini-3.1-pro-low`,
		`agentModelSorts`,
		`commandModelIds`,
		`imageGenerationModelIds`,
		`tabModelIds`,
		`defaultAgentModelId`,
		`Object.entries(e).forEach`,
		`Ta(T,k)`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("auth files quota asset missing current Antigravity catalog marker %q", want)
		}
	}
}
