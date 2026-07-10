package egress

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreMigratesAndPersistsNodeEndpointBinding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "data", "usage.db"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	node := Node{
		ID:        "17",
		Name:      "sg-01",
		Addresses: []string{"100.64.0.17", "fd7a:115c:a1e0::17"},
		Online:    true,
		LastSeen:  now,
		Tags:      []string{"tag:clirelay-egress"},
	}
	if err = store.UpsertNodes(ctx, []Node{node}, now); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		Name:             "Singapore SOCKS5",
		NodeID:           node.ID,
		Protocol:         ProtocolSOCKS5,
		Host:             "100.64.0.17",
		Port:             1080,
		Enabled:          true,
		ExpectedPublicIP: "198.51.100.44",
		Username:         "relay",
		Password:         "secret",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	if endpoint.ID == "" {
		t.Fatal("CreateEndpoint() ID is empty")
	}

	identity, err := StableIdentity("acct-123")
	if err != nil {
		t.Fatalf("StableIdentity() error = %v", err)
	}
	if want := "codex:3abf465e869e7b65598ec70e64b86462802516681a49069caa7947457c9d17aa"; identity != want {
		t.Fatalf("StableIdentity() = %q, want %q", identity, want)
	}
	if err = store.PutBinding(ctx, Binding{
		Identity:   identity,
		EndpointID: endpoint.ID,
		AuthFileID: "codex-user@example.com.json",
	}); err != nil {
		t.Fatalf("PutBinding() error = %v", err)
	}

	resolved, err := store.ResolveIdentity(ctx, identity)
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}
	if resolved.Endpoint.ID != endpoint.ID || resolved.Binding.AuthFileID != "codex-user@example.com.json" {
		t.Fatalf("ResolveIdentity() = %#v", resolved)
	}
	if resolved.Endpoint.Password != "secret" {
		t.Fatalf("ResolveIdentity() password = %q", resolved.Endpoint.Password)
	}

	counts, err := store.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts() error = %v", err)
	}
	if counts.Nodes != 1 || counts.Endpoints != 1 || counts.Bindings != 1 {
		t.Fatalf("Counts() = %#v", counts)
	}
}

func TestStoreEnforcesEndpointForeignKeys(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.CreateEndpoint(ctx, Endpoint{
		Name:             "missing node",
		NodeID:           "999",
		Protocol:         ProtocolHTTP,
		Host:             "100.64.0.99",
		Port:             8080,
		Enabled:          true,
		ExpectedPublicIP: "198.51.100.99",
	})
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("CreateEndpoint() error = %v, want ErrNodeNotFound", err)
	}

	identity, err := StableIdentity("acct-404")
	if err != nil {
		t.Fatalf("StableIdentity() error = %v", err)
	}
	err = store.PutBinding(ctx, Binding{Identity: identity, EndpointID: "missing"})
	if !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("PutBinding() error = %v, want ErrEndpointNotFound", err)
	}

	var foreignKeys int
	if err = store.DB().QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys error = %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 1", foreignKeys)
	}

	var version int
	if err = store.DB().QueryRowContext(ctx, `SELECT version FROM egress_schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("schema version query error = %v", err)
		}
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}
}

func TestUpsertNodesMarksMissingProjectionOffline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	firstSync := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	if err = store.UpsertNodes(ctx, []Node{{
		ID:        "17",
		Name:      "sg-01",
		Addresses: []string{"100.64.0.17"},
		Online:    true,
		Tags:      []string{"tag:clirelay-egress"},
	}}, firstSync); err != nil {
		t.Fatalf("first UpsertNodes() error = %v", err)
	}
	secondSync := firstSync.Add(time.Minute)
	if err = store.UpsertNodes(ctx, nil, secondSync); err != nil {
		t.Fatalf("second UpsertNodes() error = %v", err)
	}

	node, err := store.GetNode(ctx, "17")
	if err != nil {
		t.Fatalf("GetNode() error = %v", err)
	}
	if node.Online {
		t.Fatal("missing node should be marked offline")
	}
	if !node.SyncedAt.Equal(secondSync) {
		t.Fatalf("synced_at = %v, want %v", node.SyncedAt, secondSync)
	}
}

func TestStableIdentityRejectsEmptyAccountID(t *testing.T) {
	t.Parallel()

	identity, err := StableIdentity("  ")
	if !errors.Is(err, ErrEgressRequired) {
		t.Fatalf("StableIdentity() error = %v, want ErrEgressRequired", err)
	}
	if identity != "" {
		t.Fatalf("StableIdentity() = %q, want empty", identity)
	}
}

func TestUpdateEndpointInvalidatesHealthWhenRouteChanges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err = store.UpsertNodes(ctx, []Node{{
		ID: "n1", Addresses: []string{"100.64.0.1", "100.64.0.2"}, Online: true, Tags: []string{"tag:clirelay-egress"},
	}}, now); err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		Name: "one", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.1", Port: 1080,
		Enabled: true, Username: "relay", Password: "secret", ExpectedPublicIP: "198.51.100.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err = store.UpdateEndpointCheck(ctx, endpoint.ID, endpoint.ExpectedPublicIP, EndpointStatusHealthy, "", 42, now)
	if err != nil {
		t.Fatal(err)
	}

	endpoint.Name = "renamed"
	endpoint, err = store.UpdateEndpoint(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.CheckStatus != EndpointStatusHealthy || endpoint.PublicIP == "" || endpoint.LastCheckedAt.IsZero() {
		t.Fatalf("name-only update unexpectedly invalidated health: %#v", endpoint)
	}

	endpoint.Host = "100.64.0.2"
	endpoint, err = store.UpdateEndpoint(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.PublicIP != "" || endpoint.LatencyMS != 0 || !endpoint.LastCheckedAt.IsZero() || endpoint.CheckStatus != "" || endpoint.CheckError != "" {
		t.Fatalf("route update inherited stale health: %#v", endpoint)
	}
}

func TestUpdateEndpointInvalidatesHealthWhenReEnabled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	_ = store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{"100.64.0.1"}, Online: true, Tags: []string{"tag:clirelay-egress"}}}, now)
	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		Name: "one", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.1", Port: 1080,
		Enabled: true, ExpectedPublicIP: "198.51.100.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ = store.UpdateEndpointCheck(ctx, endpoint.ID, endpoint.ExpectedPublicIP, EndpointStatusHealthy, "", 1, now)
	endpoint.Enabled = false
	endpoint, err = store.UpdateEndpoint(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Enabled = true
	endpoint, err = store.UpdateEndpoint(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.CheckStatus != "" || endpoint.PublicIP != "" || !endpoint.LastCheckedAt.IsZero() {
		t.Fatalf("re-enabled endpoint inherited stale health: %#v", endpoint)
	}
}
