package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFiles_IncludesProjectIDFromManager(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "antigravity-user@example.com-project-a.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"antigravity","email":"user@example.com","project_id":"project-a"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type":       "antigravity",
			"email":      "user@example.com",
			"project_id": "project-a",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	entry := firstAuthFileEntry(t, h)
	if got := entry["project_id"]; got != "project-a" {
		t.Fatalf("expected project_id %q, got %#v", "project-a", got)
	}
}

func TestListAuthFilesFromDisk_IncludesProjectID(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "antigravity-user@example.com-project-a.json")
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"antigravity","email":"user@example.com","project_id":"project-a"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	entry := firstAuthFileEntry(t, h)
	if got := entry["project_id"]; got != "project-a" {
		t.Fatalf("expected project_id %q, got %#v", "project-a", got)
	}
}

func TestListAuthFiles_IncludesWebsocketsFromManager(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-user@example.com-pro.json"
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
			"path":       filePath,
			"websockets": "true",
		},
		Metadata: map[string]any{
			"type": "codex",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	entry := firstAuthFileEntry(t, h)
	if got := entry["websockets"]; got != true {
		t.Fatalf("expected websockets true, got %#v", got)
	}
}

func TestListAuthFilesFromDisk_IncludesWebsockets(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "codex-user@example.com-pro.json")
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","email":"user@example.com","websockets":false}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	entry := firstAuthFileEntry(t, h)
	if got := entry["websockets"]; got != false {
		t.Fatalf("expected websockets false, got %#v", got)
	}
}

func TestListAuthFiles_IncludesCodexTokenHealthFromManager(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	expiresAt := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	lastRefresh := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	fileName := "codex-user@example.com-pro.json"
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
			"type":         "codex",
			"email":        "user@example.com",
			"expired":      expiresAt,
			"last_refresh": lastRefresh,
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	entry := firstAuthFileEntry(t, h)
	if got := entry["token_health"]; got != "warning" {
		t.Fatalf("expected token_health warning, got %#v", got)
	}
	if _, ok := entry["token_expires_at"].(string); !ok {
		t.Fatalf("expected token_expires_at string, got %#v", entry["token_expires_at"])
	}
	if secondsLeft, ok := entry["token_seconds_left"].(float64); !ok || secondsLeft <= 0 {
		t.Fatalf("expected positive token_seconds_left, got %#v", entry["token_seconds_left"])
	}
	if _, ok := entry["token_last_refresh"].(string); !ok {
		t.Fatalf("expected token_last_refresh string, got %#v", entry["token_last_refresh"])
	}
}

func TestListAuthFilesFromDisk_IncludesCodexTokenHealth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	expiresAt := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	lastRefresh := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	filePath := filepath.Join(authDir, "codex-user@example.com-pro.json")
	payload := `{"type":"codex","email":"user@example.com","expired":"` + expiresAt + `","last_refresh":"` + lastRefresh + `"}`
	if errWrite := os.WriteFile(filePath, []byte(payload), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	entry := firstAuthFileEntry(t, h)
	if got := entry["token_health"]; got != "warning" {
		t.Fatalf("expected token_health warning, got %#v", got)
	}
	if _, ok := entry["token_expires_at"].(string); !ok {
		t.Fatalf("expected token_expires_at string, got %#v", entry["token_expires_at"])
	}
	if _, ok := entry["token_last_refresh"].(string); !ok {
		t.Fatalf("expected token_last_refresh string, got %#v", entry["token_last_refresh"])
	}
}

func TestAddAuthFileTokenHealth_StatusThresholdsAndRedaction(t *testing.T) {
	now := time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		provider  string
		disabled  bool
		expiresAt time.Time
		want      string
		wantField bool
	}{
		{
			name:      "disabled wins",
			provider:  "codex",
			disabled:  true,
			expiresAt: now.Add(-time.Hour),
			want:      "disabled",
			wantField: true,
		},
		{
			name:      "expired",
			provider:  "codex",
			expiresAt: now,
			want:      "expired",
			wantField: true,
		},
		{
			name:      "critical",
			provider:  "codex",
			expiresAt: now.Add(24 * time.Hour),
			want:      "critical",
			wantField: true,
		},
		{
			name:      "warning",
			provider:  "codex",
			expiresAt: now.Add(72 * time.Hour),
			want:      "warning",
			wantField: true,
		},
		{
			name:      "ok",
			provider:  "codex",
			expiresAt: now.Add(73 * time.Hour),
			want:      "ok",
			wantField: true,
		},
		{
			name:      "non codex ignored",
			provider:  "openai",
			expiresAt: now.Add(time.Hour),
			wantField: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := gin.H{}
			metadata := map[string]any{
				"expired":       tt.expiresAt.Format(time.RFC3339),
				"last_refresh":  now.Add(-time.Hour).Format(time.RFC3339),
				"access_token":  "access-secret",
				"refresh_token": "refresh-secret",
				"id_token":      "id-secret",
			}

			addAuthFileTokenHealth(entry, tt.provider, metadata, tt.disabled, now)

			if !tt.wantField {
				if len(entry) != 0 {
					t.Fatalf("expected no derived fields, got %#v", entry)
				}
				return
			}
			if got := entry["token_health"]; got != tt.want {
				t.Fatalf("expected token_health %q, got %#v", tt.want, got)
			}
			for _, secretKey := range []string{"access_token", "refresh_token", "id_token"} {
				if _, ok := entry[secretKey]; ok {
					t.Fatalf("raw secret key %q leaked in entry: %#v", secretKey, entry)
				}
			}
			if _, ok := entry["token_expires_at"].(time.Time); !ok {
				t.Fatalf("expected token_expires_at time.Time, got %#v", entry["token_expires_at"])
			}
			if _, ok := entry["token_expires_at_ms"].(int64); !ok {
				t.Fatalf("expected token_expires_at_ms int64, got %#v", entry["token_expires_at_ms"])
			}
			if _, ok := entry["token_last_refresh"].(time.Time); !ok {
				t.Fatalf("expected token_last_refresh time.Time, got %#v", entry["token_last_refresh"])
			}
		})
	}
}

func firstAuthFileEntry(t *testing.T, h *Handler) map[string]any {
	t.Helper()

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	filesRaw, ok := payload["files"].([]any)
	if !ok {
		t.Fatalf("expected files array, payload: %#v", payload)
	}
	if len(filesRaw) != 1 {
		t.Fatalf("expected 1 auth entry, got %d", len(filesRaw))
	}
	fileEntry, ok := filesRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("expected file entry object, got %#v", filesRaw[0])
	}
	return fileEntry
}
