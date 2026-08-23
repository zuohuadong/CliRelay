package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCodexTokenStorageSaveTokenToFileReplacesExistingCredentialPrivately(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"old"}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("make auth file read-only: %v", err)
	}

	storage := &CodexTokenStorage{
		IDToken:      "id-token",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		AccountID:    "account-1",
		LastRefresh:  "2026-07-22T12:34:56Z",
		Metadata: map[string]any{
			"agent_private_key": "private-key",
		},
	}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile() error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if !bytes.HasSuffix(raw, []byte("\n")) {
		t.Fatal("Codex credential file must end with a newline")
	}
	var metadata map[string]any
	if err = json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if metadata["access_token"] != "access-token" || metadata["refresh_token"] != "refresh-token" || metadata["id_token"] != "id-token" {
		t.Fatalf("OAuth token bundle was not preserved: %#v", metadata)
	}
	if metadata["agent_private_key"] != "private-key" {
		t.Fatalf("agent_private_key = %#v", metadata["agent_private_key"])
	}
	assertCodexPrivateCredentialMode(t, path)
	assertNoCodexCredentialTempFiles(t, dir, "codex.json")
}

func TestCodexTokenStorageSaveTokenToFileTightensSameCredential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	storage := &CodexTokenStorage{
		IDToken:      "id-token",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		AccountID:    "account-1",
		LastRefresh:  "2026-07-22T12:34:56Z",
	}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("first SaveTokenToFile() error: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("loosen auth file permissions: %v", err)
	}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("second SaveTokenToFile() error: %v", err)
	}

	assertCodexPrivateCredentialMode(t, path)
	assertNoCodexCredentialTempFiles(t, dir, "codex.json")
}

func assertCodexPrivateCredentialMode(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential mode = %o, want 600", got)
	}
}

func assertNoCodexCredentialTempFiles(t *testing.T, dir, name string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "."+name+".tmp-*"))
	if err != nil {
		t.Fatalf("glob credential temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("credential temp files were not removed: %v", matches)
	}
}

func TestSaveTokenToFile_PreservesCustomMetadata(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "codex-test.json")

	storage := &CodexTokenStorage{
		Type:         "codex",
		Email:        "user@example.com",
		AccessToken:  "new-access-token",
		RefreshToken: "new-refresh-token",
		IDToken:      "new-id-token",
		AccountID:    "new-account",
		Expire:       "2026-12-31T23:59:59Z",
		LastRefresh:  "2026-04-14T12:00:00Z",
	}
	storage.SetMetadata(map[string]any{
		"disabled":   false,
		"prefix":     "my-prefix",
		"websockets": false,
		"note":       "my important note",
		"proxy_url":  "http://proxy:8080",
		"weight":     float64(42),
	})

	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}

	savedRaw, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("os.ReadFile error = %v", errRead)
	}

	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error = %v", errUnmarshal)
	}

	// Verify updated OAuth token fields
	if saved["access_token"] != "new-access-token" {
		t.Errorf("access_token = %v, want new-access-token", saved["access_token"])
	}
	if saved["refresh_token"] != "new-refresh-token" {
		t.Errorf("refresh_token = %v, want new-refresh-token", saved["refresh_token"])
	}
	if saved["id_token"] != "new-id-token" {
		t.Errorf("id_token = %v, want new-id-token", saved["id_token"])
	}
	if saved["account_id"] != "new-account" {
		t.Errorf("account_id = %v, want new-account", saved["account_id"])
	}

	// Verify custom fields in metadata
	if saved["prefix"] != "my-prefix" {
		t.Errorf("prefix = %v, want my-prefix", saved["prefix"])
	}
	if saved["websockets"] != false {
		t.Errorf("websockets = %v, want false", saved["websockets"])
	}
	if saved["note"] != "my important note" {
		t.Errorf("note = %v, want my important note", saved["note"])
	}
	if saved["proxy_url"] != "http://proxy:8080" {
		t.Errorf("proxy_url = %v, want http://proxy:8080", saved["proxy_url"])
	}
	if saved["weight"] != float64(42) {
		t.Errorf("weight = %v, want 42", saved["weight"])
	}
}
