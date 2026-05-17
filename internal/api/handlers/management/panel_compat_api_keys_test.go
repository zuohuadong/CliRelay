package management

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

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
		key, name, disabled, daily_limit, total_quota, spending_limit, concurrency_limit, rpm_limit, tpm_limit,
		allowed_models, allowed_channels, allowed_channel_groups, system_prompt, created_at, updated_at, permission_profile_id
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sk-test", "tester", 0, 123, 456, 7.5, 2, 10, 20, `["gpt-5"]`, `["codex"]`, `["pro"]`, "system", "2026-05-17T00:00:00Z", "2026-05-17T00:00:01Z", "profile-1")
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
