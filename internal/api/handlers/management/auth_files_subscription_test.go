package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFiles_IncludesSubscriptionMetadata(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-user@example.com.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","email":"user@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type":                    "codex",
			"email":                   "user@example.com",
			"subscription_started_at": "2026-04-01T00:00:00Z",
			"subscription_period":     "monthly",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	entry := firstAuthFileEntry(t, h)
	if got := entry["subscription_started_at"]; got != "2026-04-01T00:00:00Z" {
		t.Fatalf("subscription_started_at = %#v", got)
	}
	if got := entry["subscription_period"]; got != "monthly" {
		t.Fatalf("subscription_period = %#v", got)
	}
}

func TestListAuthFiles_IncludesCodexOAuthSubscriptionFromIDToken(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	until := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	start := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	idToken := testCodexSubscriptionIDToken(t, start.Unix(), until.Unix())

	authDir := t.TempDir()
	fileName := "codex-oauth.json"
	filePath := filepath.Join(authDir, fileName)
	payload, err := json.Marshal(map[string]any{
		"type":     "codex",
		"email":    "user@example.com",
		"id_token": idToken,
	})
	if err != nil {
		t.Fatalf("marshal auth file: %v", err)
	}
	if errWrite := os.WriteFile(filePath, payload, 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type":     "codex",
			"email":    "user@example.com",
			"id_token": idToken,
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	entry := firstAuthFileEntry(t, h)
	assertCodexSubscriptionUntil(t, entry, until)
}

func TestListAuthFilesFromDisk_IncludesCodexOAuthSubscriptionFromIDToken(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	until := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	start := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	idToken := testCodexSubscriptionIDToken(t, start.Unix(), until.Unix())

	authDir := t.TempDir()
	payload, err := json.Marshal(map[string]any{
		"type":     "codex",
		"email":    "user@example.com",
		"id_token": idToken,
	})
	if err != nil {
		t.Fatalf("marshal auth file: %v", err)
	}
	if errWrite := os.WriteFile(filepath.Join(authDir, "codex-oauth.json"), payload, 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	entry := firstAuthFileEntry(t, h)
	assertCodexSubscriptionUntil(t, entry, until)
}

func assertCodexSubscriptionUntil(t *testing.T, entry map[string]any, until time.Time) {
	t.Helper()
	rawIDToken, ok := entry["id_token"].(map[string]any)
	if !ok {
		t.Fatalf("id_token = %#v", entry["id_token"])
	}
	rawUntil, ok := rawIDToken["chatgpt_subscription_active_until"].(string)
	if !ok {
		t.Fatalf("chatgpt_subscription_active_until = %#v", rawIDToken["chatgpt_subscription_active_until"])
	}
	parsed, err := time.Parse(time.RFC3339, rawUntil)
	if err != nil {
		t.Fatalf("parse chatgpt_subscription_active_until %q: %v", rawUntil, err)
	}
	if !parsed.Equal(until) {
		t.Fatalf("chatgpt_subscription_active_until = %s, want %s", parsed.UTC().Format(time.RFC3339), until.Format(time.RFC3339))
	}
	if entry["subscription_started_at"] != nil {
		t.Fatalf("unexpected subscription_started_at %#v", entry["subscription_started_at"])
	}
}

func testCodexSubscriptionIDToken(t *testing.T, startUnix, untilUnix int64) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal JWT header: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":                "account-1",
			"chatgpt_plan_type":                 "plus",
			"chatgpt_subscription_active_start": startUnix,
			"chatgpt_subscription_active_until": untilUnix,
		},
	})
	if err != nil {
		t.Fatalf("marshal JWT payload: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
