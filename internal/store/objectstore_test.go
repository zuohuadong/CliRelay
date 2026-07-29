package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestObjectTokenStoreMetadataFailurePreservesLocalMirror(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "object backend unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	store, err := NewObjectTokenStore(ObjectStoreConfig{
		Endpoint:  strings.TrimPrefix(server.URL, "http://"),
		Bucket:    "credentials",
		AccessKey: "access-key",
		SecretKey: "secret-key",
		LocalRoot: t.TempDir(),
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewObjectTokenStore() error = %v", err)
	}
	path := filepath.Join(store.authDir, "codex.json")
	original := []byte(`{"type":"codex","access_token":"old"}`)
	if err = os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed local mirror: %v", err)
	}
	auth := &cliproxyauth.Auth{
		ID:       "codex.json",
		FileName: "codex.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "new"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err = store.Save(ctx, auth); err == nil {
		t.Fatal("Save() succeeded while object backend was unavailable")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read local mirror after failed Save: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("local mirror changed after failed Save: %s", got)
	}
}
