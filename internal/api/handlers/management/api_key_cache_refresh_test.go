package management

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	configaccess "github.com/router-for-me/CLIProxyAPI/v6/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)

func TestRefreshAPIKeyCacheUpdatesLiveAccessManager(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "usage-keys-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	dbPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer func() {
		usage.CloseDB()
		_ = os.Remove(dbPath)
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
	}()

	if err := usage.InitDB(dbPath, config.RequestLogStorageConfig{}, time.UTC); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	const key = "sk-test-allowed-channels"
	if err := usage.UpsertAPIKey(usage.APIKeyRow{Key: key, Name: "Test Key"}); err != nil {
		t.Fatalf("UpsertAPIKey (initial): %v", err)
	}

	cfg := &config.Config{}
	accessManager := sdkaccess.NewManager()

	// Prime accessManager with the initial provider snapshot (no allowed-channels).
	// This mirrors server bootstrap where the provider instance is captured once.
	configaccess.Register(&cfg.SDKConfig)
	accessManager.SetProviders(sdkaccess.RegisteredProviders())

	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	res, authErr := accessManager.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate (before refresh): %v", authErr)
	}
	if res == nil || res.Metadata == nil {
		t.Fatalf("expected auth result before refresh")
	}
	if got := res.Metadata["allowed-channels"]; got != "" {
		t.Fatalf("expected empty allowed-channels before refresh, got %q", got)
	}

	// Update the key config in SQLite (allowed-channels set).
	if err := usage.UpsertAPIKey(usage.APIKeyRow{Key: key, Name: "Test Key", AllowedChannels: []string{"kimi"}}); err != nil {
		t.Fatalf("UpsertAPIKey (update): %v", err)
	}

	h := NewHandler(cfg, "", nil)
	h.SetAccessManager(accessManager)
	h.refreshAPIKeyCache()

	res, authErr = accessManager.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate (after refresh): %v", authErr)
	}
	if res == nil || res.Metadata == nil {
		t.Fatalf("expected auth result after refresh")
	}
	if got := res.Metadata["allowed-channels"]; got != "kimi" {
		t.Fatalf("allowed-channels = %q, want %q", got, "kimi")
	}
}
