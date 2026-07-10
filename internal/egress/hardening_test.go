package egress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func testStableIdentity(accountID string) string {
	identity, _ := StableIdentity(accountID)
	return identity
}

func TestOpenStoreHardensPermissionsAndRejectsSecondWriter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not authoritative on Windows")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dataDir, "egress.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	assertPerm := func(name string, want os.FileMode) {
		t.Helper()
		info, statErr := os.Stat(name)
		if statErr != nil {
			t.Fatalf("Stat(%s) error = %v", name, statErr)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %o, want %o", name, got, want)
		}
	}
	assertPerm(dataDir, 0o700)
	assertPerm(path, 0o600)
	assertPerm(path+".lock", 0o600)

	if _, err = OpenStore(path); !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("second OpenStore() error = %v, want ErrStoreLocked", err)
	}
	if err = store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() after close error = %v", err)
	}
	_ = reopened.Close()
}

func TestStoreExclusiveBindingBatchIsAtomicAndRevisioned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err = store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{"100.64.0.1"}, Online: true, Tags: []string{"tag:clirelay-egress"}}}, now); err != nil {
		t.Fatal(err)
	}
	e1, err := store.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.1", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.PreviewBindingBatch(ctx, []BindingAssignment{{Identity: testStableIdentity("a"), EndpointID: e1.ID}})
	if err != nil || !preview.Valid || preview.Revision == "" {
		t.Fatalf("PreviewBindingBatch() = %#v, %v", preview, err)
	}
	result, err := store.ApplyBindingBatch(ctx, preview.Revision, preview.Assignments)
	if err != nil || result.Applied != 1 || result.Revision == preview.Revision {
		t.Fatalf("ApplyBindingBatch() = %#v, %v", result, err)
	}

	conflict, err := store.PreviewBindingBatch(ctx, []BindingAssignment{{Identity: testStableIdentity("b"), EndpointID: e1.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Valid || len(conflict.Conflicts) == 0 {
		t.Fatalf("exclusive conflict preview = %#v", conflict)
	}
	if _, err = store.ApplyBindingBatch(ctx, preview.Revision, []BindingAssignment{{Identity: testStableIdentity("b"), EndpointID: e1.ID}}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale ApplyBindingBatch() error = %v", err)
	}
	bindings, _ := store.ListBindings(ctx)
	if len(bindings) != 1 || bindings[0].Identity != testStableIdentity("a") {
		t.Fatalf("bindings after rejected batches = %#v", bindings)
	}
}

func TestStoreConcurrentBatchOnlyOneRevisionWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	_ = store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{"100.64.0.1", "100.64.0.2"}, Online: true, Tags: []string{"tag:clirelay-egress"}}}, now)
	e1, _ := store.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.1", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	e2, _ := store.CreateEndpoint(ctx, Endpoint{Name: "two", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.2", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.2"})
	preview, _ := store.PreviewBindingBatch(ctx, nil)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, assignment := range []BindingAssignment{{Identity: testStableIdentity("a"), EndpointID: e1.ID}, {Identity: testStableIdentity("b"), EndpointID: e2.ID}} {
		wg.Add(1)
		go func(a BindingAssignment) {
			defer wg.Done()
			_, applyErr := store.ApplyBindingBatch(ctx, preview.Revision, []BindingAssignment{a})
			errs <- applyErr
		}(assignment)
	}
	wg.Wait()
	close(errs)
	success, stale := 0, 0
	for applyErr := range errs {
		switch {
		case applyErr == nil:
			success++
		case errors.Is(applyErr, ErrRevisionConflict):
			stale++
		default:
			t.Fatalf("unexpected apply error: %v", applyErr)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
}

func TestStoreBindingBatchAtomicallyDeletesAndReassigns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err = store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{"100.64.0.1", "100.64.0.2"}, Online: true, Tags: []string{"tag:clirelay-egress"}}}, now); err != nil {
		t.Fatal(err)
	}
	e1, _ := store.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.1", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	e2, _ := store.CreateEndpoint(ctx, Endpoint{Name: "two", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.2", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.2"})
	if _, err = store.ApplyBindingBatch(ctx, "", []BindingAssignment{{Identity: testStableIdentity("a"), EndpointID: e1.ID}, {Identity: testStableIdentity("b"), EndpointID: e2.ID}}); err != nil {
		t.Fatal(err)
	}

	assignments := []BindingAssignment{{Identity: testStableIdentity("a"), EndpointID: ""}, {Identity: testStableIdentity("b"), EndpointID: e1.ID}}
	preview, err := store.PreviewBindingBatch(ctx, assignments)
	containsUnbind := false
	for _, assignment := range preview.Assignments {
		containsUnbind = containsUnbind || assignment.EndpointID == ""
	}
	if err != nil || !preview.Valid || len(preview.Assignments) != 2 || !containsUnbind {
		t.Fatalf("unbind preview = %#v, %v", preview, err)
	}
	result, err := store.ApplyBindingBatch(ctx, preview.Revision, preview.Assignments)
	if err != nil || result.Applied != 2 {
		t.Fatalf("ApplyBindingBatch() = %#v, %v", result, err)
	}
	bindings, err := store.ListBindings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].Identity != testStableIdentity("b") || bindings[0].EndpointID != e1.ID {
		t.Fatalf("bindings after unbind/reassign = %#v", bindings)
	}
}

func TestStoreBindingBatchRejectsInvalidDeleteWithoutPartialMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	_ = store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{"100.64.0.1"}, Online: true, Tags: []string{"tag:clirelay-egress"}}}, now)
	endpoint, _ := store.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.1", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	_, _ = store.ApplyBindingBatch(ctx, "", []BindingAssignment{{Identity: testStableIdentity("a"), EndpointID: endpoint.ID}})
	preview, err := store.PreviewBindingBatch(ctx, []BindingAssignment{{Identity: testStableIdentity("a"), EndpointID: ""}, {Identity: "", EndpointID: ""}})
	if err != nil || preview.Valid || len(preview.Conflicts) == 0 || preview.Conflicts[0].Code != "invalid_assignment" {
		t.Fatalf("invalid delete preview = %#v, %v", preview, err)
	}
	if _, err = store.ApplyBindingBatch(ctx, preview.Revision, preview.Assignments); !errors.Is(err, ErrEndpointInvalid) {
		t.Fatalf("invalid delete apply error = %v", err)
	}
	bindings, _ := store.ListBindings(ctx)
	if len(bindings) != 1 || bindings[0].Identity != testStableIdentity("a") || bindings[0].EndpointID != endpoint.ID {
		t.Fatalf("invalid batch partially mutated bindings: %#v", bindings)
	}
}

func TestEndpointImpactAllowsConfirmedDisableButProtectsBoundDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	_ = store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{"100.64.0.1"}, Online: true, Tags: []string{"tag:clirelay-egress"}}}, now)
	endpoint, _ := store.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.1", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	if err = store.PutBinding(ctx, Binding{Identity: testStableIdentity("a"), EndpointID: endpoint.ID}); err != nil {
		t.Fatal(err)
	}
	disableImpact, err := store.EndpointImpact(ctx, endpoint.ID, EndpointActionDisable)
	if err != nil {
		t.Fatal(err)
	}
	if !disableImpact.Allowed || !disableImpact.RequiresConfirmation || disableImpact.BindingCount != 1 {
		t.Fatalf("disable impact = %#v", disableImpact)
	}
	deleteImpact, err := store.EndpointImpact(ctx, endpoint.ID, EndpointActionDelete)
	if err != nil {
		t.Fatal(err)
	}
	if deleteImpact.Allowed || len(deleteImpact.Blockers) == 0 {
		t.Fatalf("delete impact = %#v", deleteImpact)
	}
	if err = store.ApplyEndpointAction(ctx, endpoint.ID, EndpointActionDisable, false, disableImpact.Revision); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("unconfirmed disable error = %v", err)
	}
	if err = store.ApplyEndpointAction(ctx, endpoint.ID, EndpointActionDisable, true, disableImpact.Revision); err != nil {
		t.Fatalf("confirmed disable error = %v", err)
	}
	disabled, _ := store.GetEndpoint(ctx, endpoint.ID)
	if disabled.Enabled {
		t.Fatal("endpoint remains enabled")
	}
	if err = store.ApplyEndpointAction(ctx, endpoint.ID, EndpointActionDelete, true, ""); !errors.Is(err, ErrEndpointInUse) {
		t.Fatalf("bound delete error = %v", err)
	}
}

func TestStoreBindingBatchCanAtomicallySwapExclusiveEndpoints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	_ = store.UpsertNodes(ctx, []Node{{ID: "n1", Addresses: []string{"100.64.0.1", "100.64.0.2"}, Online: true, Tags: []string{"tag:clirelay-egress"}}}, now)
	e1, _ := store.CreateEndpoint(ctx, Endpoint{Name: "one", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.1", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.1"})
	e2, _ := store.CreateEndpoint(ctx, Endpoint{Name: "two", NodeID: "n1", Protocol: ProtocolSOCKS5, Host: "100.64.0.2", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.2"})
	_ = store.PutBinding(ctx, Binding{Identity: testStableIdentity("a"), EndpointID: e1.ID})
	_ = store.PutBinding(ctx, Binding{Identity: testStableIdentity("b"), EndpointID: e2.ID})
	assignments := []BindingAssignment{{Identity: testStableIdentity("a"), EndpointID: e2.ID}, {Identity: testStableIdentity("b"), EndpointID: e1.ID}}
	preview, err := store.PreviewBindingBatch(ctx, assignments)
	if err != nil || !preview.Valid {
		t.Fatalf("swap preview = %#v, %v", preview, err)
	}
	if _, err = store.ApplyBindingBatch(ctx, preview.Revision, assignments); err != nil {
		t.Fatalf("swap apply error = %v", err)
	}
	a, _ := store.ResolveIdentity(ctx, testStableIdentity("a"))
	b, _ := store.ResolveIdentity(ctx, testStableIdentity("b"))
	if a.Binding.EndpointID != e2.ID || b.Binding.EndpointID != e1.ID {
		t.Fatalf("swap result a=%#v b=%#v", a.Binding, b.Binding)
	}
}
