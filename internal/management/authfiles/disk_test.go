package authfiles

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListDiskEntriesBuildsPresentationFields(t *testing.T) {
	authDir := t.TempDir()
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	startedAt := now.AddDate(0, -1, 0).Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(authDir, "codex.json"), []byte(`{
		"type": "codex",
		"email": "user@example.com",
		"subscription_started_at": "`+startedAt+`",
		"subscription_period": "monthly"
	}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "notes.txt"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write non-json file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(authDir, "nested.json"), 0o755); err != nil {
		t.Fatalf("make nested dir: %v", err)
	}

	got, err := ListDiskEntries(authDir, now)
	if err != nil {
		t.Fatalf("ListDiskEntries() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListDiskEntries() length = %d, want 1: %#v", len(got), got)
	}
	entry := got[0]
	if entry["name"] != "codex.json" {
		t.Fatalf("name = %#v, want codex.json", entry["name"])
	}
	if entry["type"] != "codex" {
		t.Fatalf("type = %#v, want codex", entry["type"])
	}
	if entry["email"] != "user@example.com" {
		t.Fatalf("email = %#v, want user@example.com", entry["email"])
	}
	if entry["subscription_period"] != "monthly" {
		t.Fatalf("subscription_period = %#v, want monthly", entry["subscription_period"])
	}
	if _, ok := entry["subscription_expires_at"]; !ok {
		t.Fatalf("subscription_expires_at missing from %#v", entry)
	}
}

func TestListDiskEntriesReturnsReadDirError(t *testing.T) {
	_, err := ListDiskEntries(filepath.Join(t.TempDir(), "missing"), time.Time{})
	if err == nil {
		t.Fatal("ListDiskEntries() error = nil, want error")
	}
}
