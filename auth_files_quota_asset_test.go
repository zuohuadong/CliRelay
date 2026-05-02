package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const managementAssetCacheBust = "?v=issue77-antigravity-quota"

func readActiveAuthFilesAsset(t *testing.T) (string, string) {
	t.Helper()

	indexData, err := os.ReadFile("assets/index-Byn9cpqP.js")
	if err != nil {
		t.Fatalf("read main asset: %v", err)
	}
	matches := regexp.MustCompile(`AuthFilesPage-[A-Za-z0-9_-]+\.js`).FindAllString(string(indexData), -1)
	seen := make(map[string]bool)
	var names []string
	for _, match := range matches {
		if !seen[match] {
			seen[match] = true
			names = append(names, match)
		}
	}
	if len(names) != 1 {
		t.Fatalf("main asset should reference exactly one AuthFilesPage chunk, got %v", names)
	}
	path := filepath.Join("assets", names[0])
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read active auth files asset %s: %v", path, err)
	}
	return names[0], string(data)
}

func TestAuthFilesQuotaAssetSupportsAnthropicOAuthUsage(t *testing.T) {
	_, content := readActiveAuthFilesAsset(t)

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
	_, content := readActiveAuthFilesAsset(t)
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
	_, content := readActiveAuthFilesAsset(t)

	for _, want := range []string{
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

func TestAuthFilesQuotaAssetShowsAntigravityModelMetrics(t *testing.T) {
	_, content := readActiveAuthFilesAsset(t)

	for _, want := range []string{
		`maxTokens`,
		`maxOutputTokens`,
		`apiProvider`,
		`modelProvider`,
		`modelCatalogMeta`,
		`id:z.id`,
		`meta:z.meta`,
		`grid-cols-[minmax(10rem,1fr)_minmax(8rem,1fr)_5rem_8.25rem]`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("auth files quota asset missing Antigravity model metrics marker %q", want)
		}
	}
	if strings.Contains(content, `grid-cols-[3.25rem_1fr_3.25rem_8.25rem]`) {
		t.Fatal("auth files quota asset still truncates quota metric labels to 3.25rem")
	}
}

func TestAuthFilesQuotaAssetDoesNotFallBackToStaticAntigravityBuckets(t *testing.T) {
	_, content := readActiveAuthFilesAsset(t)

	for _, stale := range []string{
		`Sa=[{id:"claude-gpt"`,
		`label:"Claude/GPT"`,
		`label:"Gemini 3 Pro"`,
		`Sa.forEach`,
	} {
		if strings.Contains(content, stale) {
			t.Fatalf("auth files quota asset still uses static Antigravity quota bucket %q", stale)
		}
	}
	for _, want := range []string{
		`defaultAgentModelId`,
		`agentModelSorts`,
		`commandModelIds`,
		`tabModelIds`,
		`imageGenerationModelIds`,
		`mqueryModelIds`,
		`webSearchModelIds`,
		`commitMessageModelIds`,
		`Object.entries(e).forEach`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("auth files quota asset missing dynamic Antigravity catalog marker %q", want)
		}
	}
}

func TestManagementIndexReferencesFreshAuthFilesQuotaAsset(t *testing.T) {
	name, content := readActiveAuthFilesAsset(t)
	if name == "AuthFilesPage-8ofG866A.js" {
		t.Fatalf("main asset still references previously cached auth files chunk %s", name)
	}

	for _, stale := range []string{
		`Sa=[{id:"claude-gpt"`,
		`label:"Claude/GPT"`,
		`label:"Gemini 3 Pro"`,
		`Sa.forEach`,
	} {
		if strings.Contains(content, stale) {
			t.Fatalf("fresh auth files asset still embeds stale Antigravity quota card logic %q", stale)
		}
	}
}

func TestManagementEntryAssetsBustCachedAuthFilesBundle(t *testing.T) {
	for _, htmlPath := range []string{"manage.html", "management.html"} {
		data, err := os.ReadFile(htmlPath)
		if err != nil {
			t.Fatalf("read %s: %v", htmlPath, err)
		}
		content := string(data)
		for _, want := range []string{
			`/manage/assets/manage-WL7l-ZQz.js` + managementAssetCacheBust,
			`/manage/assets/index-Byn9cpqP.js` + managementAssetCacheBust,
		} {
			if !strings.Contains(content, want) {
				t.Fatalf("%s missing cache-busted management asset reference %q", htmlPath, want)
			}
		}
	}

	manageData, err := os.ReadFile("assets/manage-WL7l-ZQz.js")
	if err != nil {
		t.Fatalf("read manage asset: %v", err)
	}
	if !strings.Contains(string(manageData), `./index-Byn9cpqP.js`+managementAssetCacheBust) {
		t.Fatalf("manage asset should import cache-busted index asset")
	}

	_, authContent := readActiveAuthFilesAsset(t)
	if !strings.Contains(authContent, `./index-Byn9cpqP.js`+managementAssetCacheBust) {
		t.Fatalf("auth files asset should import the same cache-busted index asset")
	}
}
