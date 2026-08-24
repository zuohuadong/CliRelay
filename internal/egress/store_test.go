package egress

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreCreatesEndpointBindingOnlySchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, table := range []string{"egress_nodes", "egress_state"} {
		var count int
		if err = store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("legacy table %s must not exist", table)
		}
	}

	rows, err := store.DB().QueryContext(ctx, `PRAGMA table_info(egress_endpoints)`)
	if err != nil {
		t.Fatalf("query endpoint columns: %v", err)
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan endpoint column: %v", err)
		}
		columns[name] = struct{}{}
	}
	for _, legacyColumn := range []string{"node_id", "local_server"} {
		if _, ok := columns[legacyColumn]; ok {
			t.Fatalf("legacy endpoint column %s must not exist", legacyColumn)
		}
	}

	var version int
	if err = store.DB().QueryRowContext(ctx, `SELECT version FROM egress_schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version); err != nil {
		t.Fatalf("schema version query error = %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
}

func TestStorePersistsEndpointAndExclusiveBinding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "data", "egress.db"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		Name:             "Singapore SOCKS5",
		Protocol:         ProtocolSOCKS5,
		Host:             "10.77.0.2",
		Port:             1080,
		Enabled:          true,
		ExpectedPublicIP: "198.51.100.44",
		Username:         "relay",
		Password:         "secret",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	identity, err := StableIdentity("acct-123")
	if err != nil {
		t.Fatalf("StableIdentity() error = %v", err)
	}
	if err = store.PutBinding(ctx, Binding{Identity: identity, EndpointID: endpoint.ID, AuthFileID: "codex-user@example.com.json"}); err != nil {
		t.Fatalf("PutBinding() error = %v", err)
	}

	resolved, err := store.ResolveIdentity(ctx, identity)
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}
	if resolved.Endpoint.ID != endpoint.ID || resolved.Endpoint.Password != "secret" || resolved.Binding.AuthFileID != "codex-user@example.com.json" {
		t.Fatalf("ResolveIdentity() = %#v", resolved)
	}

	secondIdentity, _ := StableIdentity("acct-456")
	preview, err := store.PreviewBindingBatch(ctx, []BindingAssignment{{Identity: secondIdentity, EndpointID: endpoint.ID}})
	if err != nil {
		t.Fatalf("PreviewBindingBatch() error = %v", err)
	}
	if preview.Valid || len(preview.Conflicts) != 1 || preview.Conflicts[0].Code != "endpoint_already_bound" {
		t.Fatalf("exclusive binding preview = %#v", preview)
	}

	counts, err := store.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts() error = %v", err)
	}
	if counts.Endpoints != 1 || counts.EnabledEndpoints != 1 || counts.Bindings != 1 {
		t.Fatalf("Counts() = %#v", counts)
	}
}

func TestStoreAllowsMultipleBindingsForSharedEndpoint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		Name:        "Shared SOCKS5",
		Protocol:    ProtocolSOCKS5,
		Host:        "10.77.0.2",
		Port:        1080,
		Enabled:     true,
		SharingMode: EndpointSharingModeShared,
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	first, _ := StableIdentity("acct-shared-one")
	second, _ := StableIdentity("acct-shared-two")
	preview, err := store.PreviewBindingBatch(ctx, []BindingAssignment{
		{Identity: first, EndpointID: endpoint.ID},
		{Identity: second, EndpointID: endpoint.ID},
	})
	if err != nil || !preview.Valid {
		t.Fatalf("PreviewBindingBatch() = %#v, %v", preview, err)
	}
	if _, err = store.ApplyBindingBatch(ctx, preview.Revision, preview.Assignments); err != nil {
		t.Fatalf("ApplyBindingBatch() error = %v", err)
	}
	bindings, err := store.ListBindings(ctx)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("ListBindings() = %#v, %v", bindings, err)
	}
}

func TestStoreAllowsAntigravityToShareExclusiveCodexEndpoint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		Name: "Codex fixed", Protocol: ProtocolSOCKS5, Host: "10.77.0.2", Port: 1080,
		Enabled: true, SharingMode: EndpointSharingModeExclusive, ExpectedPublicIP: "198.51.100.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	codexIdentity, _ := StableIdentity("codex-account")
	antigravityA, _ := StableIdentityForProvider("antigravity", "antigravity-a.json")
	antigravityB, _ := StableIdentityForProvider("antigravity", "antigravity-b.json")

	assignments := []BindingAssignment{
		{Identity: codexIdentity, EndpointID: endpoint.ID},
		{Identity: antigravityA, EndpointID: endpoint.ID},
		{Identity: antigravityB, EndpointID: endpoint.ID},
	}
	preview, err := store.PreviewBindingBatch(ctx, assignments)
	if err != nil || !preview.Valid {
		t.Fatalf("preview = %#v, %v", preview, err)
	}
	if _, err = store.ApplyBindingBatch(ctx, preview.Revision, assignments); err != nil {
		t.Fatalf("ApplyBindingBatch() error = %v", err)
	}
	bindings, err := store.ListBindings(ctx)
	if err != nil || len(bindings) != 3 {
		t.Fatalf("bindings = %#v, %v", bindings, err)
	}
}

func TestStoreRejectsSharedEndpointDowngradeWithMultipleBindings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		Name:        "Shared SOCKS5",
		Protocol:    ProtocolSOCKS5,
		Host:        "10.77.0.2",
		Port:        1080,
		Enabled:     true,
		SharingMode: EndpointSharingModeShared,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := StableIdentity("acct-shared-one")
	second, _ := StableIdentity("acct-shared-two")
	preview, err := store.PreviewBindingBatch(ctx, []BindingAssignment{
		{Identity: first, EndpointID: endpoint.ID},
		{Identity: second, EndpointID: endpoint.ID},
	})
	if err != nil || !preview.Valid {
		t.Fatalf("PreviewBindingBatch() = %#v, %v", preview, err)
	}
	if _, err = store.ApplyBindingBatch(ctx, preview.Revision, preview.Assignments); err != nil {
		t.Fatalf("ApplyBindingBatch() error = %v", err)
	}

	endpoint.SharingMode = EndpointSharingModeExclusive
	endpoint.ExpectedPublicIP = "198.51.100.44"
	if _, err = store.UpdateEndpoint(ctx, endpoint); !errors.Is(err, ErrEndpointInUse) {
		t.Fatalf("UpdateEndpoint() error = %v, want ErrEndpointInUse", err)
	}
	stored, err := store.GetEndpoint(ctx, endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SharingMode != EndpointSharingModeShared || stored.ExpectedPublicIP != "" {
		t.Fatalf("endpoint changed after rejected downgrade: %#v", stored)
	}
	bindings, err := store.ListBindings(ctx)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("ListBindings() = %#v, %v", bindings, err)
	}
}

func TestStoreMigratesLegacyExclusiveBindingIndexForSharedEndpoints(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "egress.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE egress_schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE egress_endpoints (id TEXT PRIMARY KEY, name TEXT NOT NULL, protocol TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 0, username TEXT NOT NULL DEFAULT '', password TEXT NOT NULL DEFAULT '', expected_public_ip TEXT NOT NULL DEFAULT '', public_ip TEXT NOT NULL DEFAULT '', latency_ms INTEGER NOT NULL DEFAULT 0, last_checked_at TEXT NOT NULL DEFAULT '', check_status TEXT NOT NULL DEFAULT '', check_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE egress_bindings (identity TEXT PRIMARY KEY, endpoint_id TEXT NOT NULL, auth_file_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX idx_egress_bindings_endpoint_unique ON egress_bindings(endpoint_id)`,
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatalf("prepare legacy store: %v", err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var indexCount int
	if err = store.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_egress_bindings_endpoint_unique'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 0 {
		t.Fatalf("legacy exclusive binding index remains after migration")
	}
	var columnCount int
	if err = store.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('egress_endpoints') WHERE name = 'sharing_mode'`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if columnCount != 1 {
		t.Fatalf("sharing_mode column count = %d, want 1", columnCount)
	}
}

func TestStoreRejectsHTTPSEndpointProtocol(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.CreateEndpoint(ctx, Endpoint{
		Name: "Unsupported HTTPS CONNECT", Protocol: Protocol("https"), Host: "10.77.0.2", Port: 443,
		Enabled: true, ExpectedPublicIP: "198.51.100.44",
	})
	if !errors.Is(err, ErrEndpointInvalid) {
		t.Fatalf("CreateEndpoint() error = %v, want ErrEndpointInvalid", err)
	}

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		Name: "Supported HTTP CONNECT", Protocol: ProtocolHTTP, Host: "10.77.0.2", Port: 8080,
		Enabled: true, ExpectedPublicIP: "198.51.100.45",
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Protocol = Protocol("https")
	if _, err = store.UpdateEndpoint(ctx, endpoint); !errors.Is(err, ErrEndpointInvalid) {
		t.Fatalf("UpdateEndpoint() error = %v, want ErrEndpointInvalid", err)
	}
}

func TestStoreBatchRebindPreservesExistingAuthFileID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	endpointA, err := store.CreateEndpoint(ctx, Endpoint{
		Name: "A", Protocol: ProtocolSOCKS5, Host: "10.77.0.2", Port: 1080,
		Enabled: true, ExpectedPublicIP: "198.51.100.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	endpointB, err := store.CreateEndpoint(ctx, Endpoint{
		Name: "B", Protocol: ProtocolSOCKS5, Host: "10.77.0.3", Port: 1080,
		Enabled: true, ExpectedPublicIP: "198.51.100.11",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := StableIdentity("acct-preserve-auth-file")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutBinding(ctx, Binding{Identity: identity, EndpointID: endpointA.ID, AuthFileID: "codex-user.json"}); err != nil {
		t.Fatal(err)
	}

	if _, err = store.ApplyBindingBatch(ctx, "", []BindingAssignment{{Identity: identity, EndpointID: endpointB.ID}}); err != nil {
		t.Fatalf("ApplyBindingBatch() error = %v", err)
	}
	resolved, err := store.ResolveIdentity(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Binding.EndpointID != endpointB.ID {
		t.Fatalf("endpoint_id = %q, want %q", resolved.Binding.EndpointID, endpointB.ID)
	}
	if resolved.Binding.AuthFileID != "codex-user.json" {
		t.Fatalf("auth_file_id = %q, want preserved value", resolved.Binding.AuthFileID)
	}
}

func TestStoreRejectsLegacyHeadscaleDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "egress.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE egress_nodes (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = OpenStore(path)
	if err == nil || !strings.Contains(err.Error(), "legacy Headscale egress schema is unsupported") {
		t.Fatalf("OpenStore() error = %v", err)
	}
}

func TestStoreEndpointForeignKeyAndStableIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	identity, err := StableIdentity("  ")
	if !errors.Is(err, ErrEgressRequired) || identity != "" {
		t.Fatalf("StableIdentity() = %q, %v", identity, err)
	}
	identity, _ = StableIdentity("acct-404")
	err = store.PutBinding(ctx, Binding{Identity: identity, EndpointID: "missing"})
	if !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("PutBinding() error = %v", err)
	}
	var foreignKeys int
	if err = store.DB().QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, %v", foreignKeys, err)
	}
}

func TestUpdateEndpointInvalidatesHealthOnlyWhenRouteChangesOrReenabled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		Name: "one", Protocol: ProtocolSOCKS5, Host: "10.77.0.2", Port: 1080,
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
		t.Fatalf("name-only update invalidated health: %#v", endpoint)
	}

	endpoint.Host = "10.77.0.3"
	endpoint, err = store.UpdateEndpoint(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.PublicIP != "" || endpoint.CheckStatus != "" || !endpoint.LastCheckedAt.IsZero() {
		t.Fatalf("route update inherited stale health: %#v", endpoint)
	}

	endpoint.Enabled = false
	endpoint, _ = store.UpdateEndpoint(ctx, endpoint)
	endpoint.Enabled = true
	endpoint, err = store.UpdateEndpoint(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.PublicIP != "" || endpoint.CheckStatus != "" || !endpoint.LastCheckedAt.IsZero() {
		t.Fatalf("re-enabled endpoint inherited stale health: %#v", endpoint)
	}
}
