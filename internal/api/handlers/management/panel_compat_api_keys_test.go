package management

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "modernc.org/sqlite"
)

func TestAPIKeyEntriesFromDBUsesUsageDBMetadata(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "usage.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`create table api_keys (
		key text not null primary key,
		name text not null default '',
		disabled integer not null default 0,
		daily_limit integer not null default 0,
		total_quota integer not null default 0,
		spending_limit real not null default 0,
		monthly_spending_limit real not null default 0,
		billing_cycle_anchor text not null default '',
		concurrency_limit integer not null default 0,
		rpm_limit integer not null default 0,
		tpm_limit integer not null default 0,
		allowed_models text not null default '[]',
		allowed_channels text not null default '[]',
		allowed_channel_groups text not null default '[]',
		system_prompt text not null default '',
		created_at text not null default '',
		updated_at text not null default '',
		permission_profile_id text not null default ''
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = db.Exec(`insert into api_keys (
		key, name, disabled, daily_limit, total_quota, spending_limit, monthly_spending_limit, billing_cycle_anchor, concurrency_limit, rpm_limit, tpm_limit,
		allowed_models, allowed_channels, allowed_channel_groups, system_prompt, created_at, updated_at, permission_profile_id
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sk-test", "tester", 0, 123, 456, 7.5, 15.25, "2026-05-17T00:00:00Z", 2, 10, 20, `["gpt-5"]`, `["codex"]`, `["pro"]`, "system", "2026-05-17T00:00:00Z", "2026-05-17T00:00:01Z", "profile-1")
	if err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	h := &Handler{
		cfg:            &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"fallback-only"}}},
		configFilePath: filepath.Join(dir, "config.yaml"),
	}
	entries, ok := h.apiKeyEntriesFromDB()
	if !ok {
		t.Fatal("expected api key entries from db")
	}
	if len(entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Key != "sk-test" || got.Name != "tester" || got.DailyLimit != 123 || got.ConcurrencyLimit != 2 {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if got.MonthlySpendingLimit != 15.25 || got.BillingCycleAnchor != "2026-05-17T00:00:00Z" {
		t.Fatalf("billing fields = %v/%q", got.MonthlySpendingLimit, got.BillingCycleAnchor)
	}
	if len(got.AllowedModels) != 1 || got.AllowedModels[0] != "gpt-5" {
		t.Fatalf("allowed models = %#v", got.AllowedModels)
	}
	if len(got.AllowedChannelGroups) != 1 || got.AllowedChannelGroups[0] != "pro" {
		t.Fatalf("allowed channel groups = %#v", got.AllowedChannelGroups)
	}
	if got.PermissionProfileID != "profile-1" {
		t.Fatalf("permission profile = %q", got.PermissionProfileID)
	}
}

func TestPreserveMaskedAPIKeysUsesExistingSecrets(t *testing.T) {
	existing := []panelAPIKeyEntry{
		{Key: "sk-live-secret-123456", Name: "primary"},
		{Key: "sk-other-secret-abcdef", Name: "secondary"},
	}
	incoming := []panelAPIKeyEntry{
		{Key: maskAPIKey(existing[0].Key), Name: "renamed"},
		{Key: "sk-new-secret", Name: "new"},
	}

	got := preserveMaskedAPIKeys(incoming, existing)
	if got[0].Key != existing[0].Key {
		t.Fatalf("masked key was not restored: %q", got[0].Key)
	}
	if got[0].Name != "renamed" {
		t.Fatalf("entry metadata changed: %+v", got[0])
	}
	if got[1].Key != "sk-new-secret" {
		t.Fatalf("new key changed: %q", got[1].Key)
	}
}

func TestGetAPIKeyEntriesReturnsFullKeysAndRepairsMaskedDB(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	dbPath := filepath.Join(dataDir, "usage.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`create table api_keys (
		key text not null primary key,
		name text not null default '',
		disabled integer not null default 0,
		daily_limit integer not null default 0,
		total_quota integer not null default 0,
		spending_limit real not null default 0,
		concurrency_limit integer not null default 0,
		rpm_limit integer not null default 0,
		tpm_limit integer not null default 0,
		allowed_models text not null default '[]',
		allowed_channels text not null default '[]',
		allowed_channel_groups text not null default '[]',
		system_prompt text not null default '',
		created_at text not null default '',
		updated_at text not null default '',
		permission_profile_id text not null default ''
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	fullKey := "sk-live-secret-123456"
	_, err = db.Exec(`insert into api_keys (key, name, created_at) values (?, ?, ?)`,
		maskAPIKey(fullKey), "masked row", "2026-05-17T00:00:00Z")
	if err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	h := &Handler{
		cfg:            &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{fullKey}}},
		configFilePath: filepath.Join(dir, "config.yaml"),
	}

	rec := runAPIKeyEntriesRequest(t, h, http.MethodGet, "", h.GetAPIKeyEntries)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Entries []panelAPIKeyEntry `json:"api-key-entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Entries) != 1 || payload.Entries[0].Key != fullKey {
		t.Fatalf("entries = %+v, want full key", payload.Entries)
	}

	var storedKey string
	if err := db.QueryRow("select key from api_keys").Scan(&storedKey); err != nil {
		t.Fatalf("query stored key: %v", err)
	}
	if storedKey != fullKey {
		t.Fatalf("stored key = %q, want repaired full key", storedKey)
	}
}

func TestPutAPIKeyEntriesRejectsUnresolvedMaskedKeys(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "usage.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`create table api_keys (key text not null primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	h := &Handler{
		cfg:            &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"sk-real-secret-0000"}}},
		configFilePath: filepath.Join(dir, "config.yaml"),
	}

	rec := runAPIKeyEntriesRequest(t, h, http.MethodPut, `[{"key":"sk-***hrt9","name":"bad"}]`, h.PutAPIKeyEntries)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if h.cfg.APIKeys[0] != "sk-real-secret-0000" {
		t.Fatalf("config API keys changed: %#v", h.cfg.APIKeys)
	}
}

func runAPIKeyEntriesRequest(t *testing.T, h *Handler, method, body string, fn func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, "/v0/management/api-key-entries", strings.NewReader(body))
	if body != "" {
		ctx.Request.Header.Set("Content-Type", "application/json")
	}
	fn(ctx)
	return rec
}
