package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAuthFileEntryExposesChannelNameAndTags(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	auth := &coreauth.Auth{
		ID:       "codex.json",
		FileName: "codex.json",
		Provider: "codex",
		Label:    "Work Codex",
		Attributes: map[string]string{
			"path": "/tmp/codex.json",
		},
		Metadata: map[string]any{
			"plan_type":    "Pro",
			"custom_tags":  []any{"VIP Team"},
			"display_tags": []any{"codex", "vip-team"},
		},
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)

	entry := h.buildAuthFileEntry(auth)

	if got := entry["channel_name"]; got != "Work Codex" {
		t.Fatalf("channel_name = %#v, want Work Codex", got)
	}
	if got := entry["default_tags"]; !stringSliceEqual(got, []string{"codex", "pro"}) {
		t.Fatalf("default_tags = %#v, want codex/pro", got)
	}
	if got := entry["custom_tags"]; !stringSliceEqual(got, []string{"vip-team"}) {
		t.Fatalf("custom_tags = %#v, want vip-team", got)
	}
	if got := entry["display_tags"]; !stringSliceEqual(got, []string{"codex", "vip-team"}) {
		t.Fatalf("display_tags = %#v, want codex/vip-team", got)
	}
}

func TestPatchAuthFileFieldsNormalizesTagsAndLabel(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "codex.json",
		FileName: "codex.json",
		Provider: "codex",
		Metadata: map[string]any{
			"type": "codex",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"codex.json","label":" Team Codex ","custom_tags":["VIP Team","vip  team"],"display_tags":["codex","VIP Team"]}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated, ok := manager.GetByID("codex.json")
	if !ok {
		t.Fatal("updated auth not found")
	}
	if updated.Label != "Team Codex" {
		t.Fatalf("label = %q, want Team Codex", updated.Label)
	}
	if got := updated.Metadata["custom_tags"]; !stringSliceEqual(got, []string{"vip-team"}) {
		t.Fatalf("metadata custom_tags = %#v, want vip-team", got)
	}
	if got := updated.Metadata["display_tags"]; !stringSliceEqual(got, []string{"codex", "vip-team"}) {
		t.Fatalf("metadata display_tags = %#v, want codex/vip-team", got)
	}
}

func stringSliceEqual(value any, want []string) bool {
	got, ok := value.([]string)
	if !ok || len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
