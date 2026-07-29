package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTightenAuthMetadataIfMatchingRestrictsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.json")
	raw := []byte(`{"type":"codex"}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("seed auth metadata: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("loosen auth metadata permissions: %v", err)
	}

	matches, err := tightenAuthMetadataIfMatching(path, raw)
	if err != nil || !matches {
		t.Fatalf("tightenAuthMetadataIfMatching() = matches:%t error:%v", matches, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat auth metadata: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("auth metadata mode = %o, want 600", got)
	}
}
