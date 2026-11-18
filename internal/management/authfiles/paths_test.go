package authfiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateFileQueryName(t *testing.T) {
	tests := []struct {
		name        string
		requireJSON bool
		want        string
		wantErr     string
	}{
		{name: "", requireJSON: true, wantErr: "invalid name"},
		{name: "nested/auth.json", requireJSON: true, wantErr: "invalid name"},
		{name: `nested\auth.json`, requireJSON: true, wantErr: "invalid name"},
		{name: "../auth.json", requireJSON: true, wantErr: "invalid name"},
		{name: `..\auth.json`, requireJSON: true, wantErr: "invalid name"},
		{name: ".", requireJSON: false, wantErr: "invalid name"},
		{name: "auth.txt", requireJSON: true, wantErr: "name must end with .json"},
		{name: "auth.txt", requireJSON: false, want: "auth.txt"},
		{name: "auth.json", requireJSON: true, want: "auth.json"},
	}

	for _, tt := range tests {
		got, err := ValidateFileQueryName(tt.name, tt.requireJSON)
		if tt.wantErr != "" {
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("ValidateFileQueryName(%q, %t) error = %v, want %q", tt.name, tt.requireJSON, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ValidateFileQueryName(%q, %t) error = %v", tt.name, tt.requireJSON, err)
		}
		if got != tt.want {
			t.Fatalf("ValidateFileQueryName(%q, %t) = %q, want %q", tt.name, tt.requireJSON, got, tt.want)
		}
	}
}

func TestValidateUploadedFileName(t *testing.T) {
	for _, filename := range []string{"nested/auth.json", `nested\auth.json`} {
		got, err := ValidateUploadedFileName(filename)
		if err != nil {
			t.Fatalf("ValidateUploadedFileName(%q) error = %v", filename, err)
		}
		if got != "auth.json" {
			t.Fatalf("ValidateUploadedFileName(%q) = %q, want auth.json", filename, got)
		}
	}

	_, err := ValidateUploadedFileName("auth.txt")
	if err == nil || err.Error() != "file must be .json" {
		t.Fatalf("ValidateUploadedFileName() error = %v, want file must be .json", err)
	}

	_, err = ValidateUploadedFileName("../")
	if err == nil || err.Error() != "invalid name" {
		t.Fatalf("ValidateUploadedFileName() error = %v, want invalid name", err)
	}
}

func TestFilePathReturnsAbsoluteBasePath(t *testing.T) {
	authDir := t.TempDir()
	got := FilePath(authDir, "nested/auth.json")
	want := filepath.Join(authDir, "auth.json")
	if got != want {
		t.Fatalf("FilePath() = %q, want %q", got, want)
	}
}

func TestFilePathRejectsInvalidBaseNameFallback(t *testing.T) {
	authDir := t.TempDir()
	got := FilePath(authDir, "../")
	want := filepath.Join(authDir, invalidFilePathPlaceholder)
	if got != want {
		t.Fatalf("FilePath() = %q, want %q", got, want)
	}
}

func TestAuthIDForPathReturnsRelativeIDWithinAuthDir(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "codex.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	got := AuthIDForPath(authDir, path)
	if got != "codex.json" {
		t.Fatalf("AuthIDForPath() = %q, want codex.json", got)
	}
}

func TestAuthIDForPathFallsBackForExternalPath(t *testing.T) {
	authDir := t.TempDir()
	externalDir := t.TempDir()
	path := filepath.Join(externalDir, "codex.json")

	got := AuthIDForPath(authDir, path)
	if got != path {
		t.Fatalf("AuthIDForPath() = %q, want %q", got, path)
	}
}
