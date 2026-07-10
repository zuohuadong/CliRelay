package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestEgressEndpointsResponseNeverReturnsPassword(t *testing.T) {
	t.Parallel()

	handler, service := newEgressManagementHandler(t, nil)
	ctx := context.Background()
	if err := service.Store().UpsertNodes(ctx, []egress.Node{{
		ID: "17", Name: "sg-01", Addresses: []string{"100.64.0.17"}, Online: true, Tags: []string{config.DefaultEgressServiceTag},
	}}, time.Now()); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}
	if _, err := service.CreateEndpoint(ctx, egress.Endpoint{
		Name: "sg socks", NodeID: "17", Protocol: egress.ProtocolSOCKS5, Host: "100.64.0.17", Port: 1080, Enabled: true, Username: "relay", Password: "super-secret", ExpectedPublicIP: "198.51.100.17",
	}); err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/egress/endpoints", nil)
	handler.GetEgressEndpoints(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "super-secret") || strings.Contains(recorder.Body.String(), `"password"`) {
		t.Fatalf("endpoint response leaked password: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"has_credentials":true`) {
		t.Fatalf("endpoint response missing credential marker: %s", recorder.Body.String())
	}
}

func TestEgressBindingsIncludesUnboundCodexAuth(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID: "codex-user.json", Provider: "codex", Label: "user@example.com", Metadata: map[string]any{"account_id": "acct-123"},
	})
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID: "codex-missing.json", Provider: "codex", Label: "missing", Metadata: map[string]any{},
	})
	handler, _ := newEgressManagementHandler(t, manager)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/egress/bindings", nil)
	handler.GetEgressBindings(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 2 {
		t.Fatalf("items = %#v", response.Items)
	}
	var foundUnbound, foundMissing bool
	for _, item := range response.Items {
		switch item["auth_id"] {
		case "codex-user.json":
			foundUnbound = item["bound"] == false && item["identity"] == "codex:3abf465e869e7b65598ec70e64b86462802516681a49069caa7947457c9d17aa"
		case "codex-missing.json":
			foundMissing = item["bound"] == false && strings.Contains(item["error"].(string), "account_id")
		}
	}
	if !foundUnbound || !foundMissing {
		t.Fatalf("items = %#v", response.Items)
	}
}

func TestEgressOverviewAndNodesExposePanelContractWithoutAPIKey(t *testing.T) {
	t.Setenv(config.DefaultHeadscaleAPIKeyEnv, "headscale-secret")
	handler, service := newEgressManagementHandler(t, nil)
	if err := service.Store().UpsertNodes(context.Background(), []egress.Node{{
		ID: "17", Name: "sg-01", Addresses: []string{"100.64.0.17"}, Online: true, Tags: []string{config.DefaultEgressServiceTag},
	}}, time.Now()); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}

	for _, call := range []struct {
		path string
		fn   func(*gin.Context)
	}{
		{path: "/v0/management/egress/overview", fn: handler.GetEgressOverview},
		{path: "/v0/management/egress/nodes", fn: handler.GetEgressNodes},
	} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, call.path, nil)
		call.fn(c)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", call.path, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if strings.Contains(body, "headscale-secret") {
			t.Fatalf("%s leaked API key: %s", call.path, body)
		}
		if call.path == "/v0/management/egress/overview" && (!strings.Contains(body, `"api_key_configured":true`) || !strings.Contains(body, `"online_nodes":1`) || !strings.Contains(body, `"last_sync_at"`)) {
			t.Fatalf("overview contract missing fields: %s", body)
		}
		if call.path == "/v0/management/egress/overview" && !strings.Contains(body, `"enabled":true`) {
			t.Fatalf("overview contract missing enabled runtime state: %s", body)
		}
		if call.path == "/v0/management/egress/nodes" && (!strings.Contains(body, `"ip_addresses":["100.64.0.17"]`) || !strings.Contains(body, `"fresh":true`) || !strings.Contains(body, `"sync_age_seconds"`) || !strings.Contains(body, `"synced_at"`)) {
			t.Fatalf("nodes contract missing ip_addresses: %s", body)
		}
	}
}

func TestEgressOverviewReportsPreparationModeWhenRuntimeDisabled(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{EgressNetwork: config.EgressNetworkConfig{
		Enabled:   false,
		Headscale: config.HeadscaleConfig{ServiceTag: config.DefaultEgressServiceTag},
	}}
	handler, service := newEgressManagementHandlerWithConfig(t, cfg, nil)
	createReadyEgressEndpoint(t, service, "17", "100.64.0.17", "198.51.100.17")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/egress/overview", nil)

	handler.GetEgressOverview(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Enabled   bool `json:"enabled"`
		Readiness struct {
			Ready             bool             `json:"ready"`
			ReadyToEnable     bool             `json:"ready_to_enable"`
			CodexOAuthAllowed bool             `json:"codex_oauth_allowed"`
			Blockers          []map[string]any `json:"blockers"`
			Warnings          []map[string]any `json:"warnings"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Enabled {
		t.Fatalf("enabled = true, want false; body=%s", recorder.Body.String())
	}
	if !response.Readiness.Ready || !response.Readiness.ReadyToEnable || response.Readiness.CodexOAuthAllowed || len(response.Readiness.Blockers) != 0 || len(response.Readiness.Warnings) != 1 || response.Readiness.Warnings[0]["code"] != "runtime_disabled" {
		t.Fatalf("preparation readiness = %#v body=%s", response.Readiness, recorder.Body.String())
	}
}

func TestEgressNodesExposeStaleFreshnessFromServicePolicy(t *testing.T) {
	handler, service := newEgressManagementHandler(t, nil)
	if err := service.Store().UpsertNodes(context.Background(), []egress.Node{{
		ID: "stale", Name: "stale", Addresses: []string{"100.64.0.30"}, Online: true, Tags: []string{config.DefaultEgressServiceTag},
	}}, time.Now().Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	recorder := invokeEgressHandler(t, http.MethodGet, "/v0/management/egress/nodes", "", nil, handler.GetEgressNodes)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"fresh":false`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDirectDeleteEgressEndpointRequiresImpactAction(t *testing.T) {
	t.Parallel()
	handler, service := newEgressManagementHandler(t, nil)
	ctx := context.Background()
	if err := service.Store().UpsertNodes(ctx, []egress.Node{{ID: "17", Name: "sg", Addresses: []string{"100.64.0.17"}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, time.Now()); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}
	endpoint, err := service.CreateEndpoint(ctx, egress.Endpoint{Name: "sg", NodeID: "17", Protocol: egress.ProtocolSOCKS5, Host: "100.64.0.17", Port: 1080, Enabled: true, ExpectedPublicIP: "198.51.100.17"})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	if _, err = service.Store().UpdateEndpointCheck(ctx, endpoint.ID, endpoint.ExpectedPublicIP, egress.EndpointStatusHealthy, "", 1, time.Now()); err != nil {
		t.Fatalf("UpdateEndpointCheck() error = %v", err)
	}
	identity, _ := egress.StableIdentity("acct-123")
	if err = service.PutBinding(ctx, egress.Binding{Identity: identity, EndpointID: endpoint.ID}); err != nil {
		t.Fatalf("PutBinding() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodDelete, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: endpoint.ID}}
	handler.DeleteEgressEndpoint(c)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "egress_action_required") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPatchEgressEndpointCannotBypassConfirmedDisableAction(t *testing.T) {
	handler, service := newEgressManagementHandler(t, nil)
	endpoint := createReadyEgressEndpoint(t, service, "17", "100.64.0.17", "198.51.100.17")
	recorder := invokeEgressHandler(t, http.MethodPatch, "/v0/management/egress/endpoints/"+endpoint.ID, endpoint.ID, gin.H{"enabled": false}, handler.PatchEgressEndpoint)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"code":"egress_action_required"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	current, _ := service.GetEndpoint(context.Background(), endpoint.ID)
	if !current.Enabled {
		t.Fatal("PATCH disabled endpoint without impact confirmation")
	}
}

func TestEgressOverviewReportsApplicationReadinessAndAuthInventory(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range []*coreauth.Auth{
		{ID: "bound.json", Provider: "codex", Label: "bound", Metadata: map[string]any{"account_id": "acct-bound"}},
		{ID: "unbound.json", Provider: "codex", Label: "unbound", Metadata: map[string]any{"account_id": "acct-unbound"}},
		{ID: "missing.json", Provider: "codex", Label: "missing", Metadata: map[string]any{}},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	}
	handler, service := newEgressManagementHandler(t, manager)
	endpoint := createReadyEgressEndpoint(t, service, "17", "100.64.0.17", "198.51.100.17")
	identity, _ := egress.StableIdentity("acct-bound")
	if err := service.PutBinding(context.Background(), egress.Binding{Identity: identity, EndpointID: endpoint.ID, AuthFileID: "bound.json"}); err != nil {
		t.Fatalf("PutBinding() error = %v", err)
	}

	recorder := invokeEgressHandler(t, http.MethodGet, "/v0/management/egress/overview", "", nil, handler.GetEgressOverview)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Scope  string `json:"scope"`
		Policy struct {
			BindingMode            string `json:"binding_mode"`
			FailureMode            string `json:"failure_mode"`
			HostKillSwitchEnforced bool   `json:"host_kill_switch_enforced"`
		} `json:"policy"`
		Counts    map[string]int `json:"counts"`
		Readiness struct {
			Ready   bool     `json:"ready"`
			Reasons []string `json:"reasons"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Scope != "application_egress" || response.Policy.BindingMode != "exclusive" || response.Policy.FailureMode != "fail_closed" || response.Policy.HostKillSwitchEnforced {
		t.Fatalf("unexpected policy contract: %#v", response)
	}
	if response.Counts["codex_auths"] != 3 || response.Counts["bound_codex_auths"] != 1 || response.Counts["unbound_codex_auths"] != 1 || response.Counts["missing_account_id"] != 1 {
		t.Fatalf("unexpected auth counts: %#v", response.Counts)
	}
	if response.Readiness.Ready || !containsString(response.Readiness.Reasons, "unbound_codex_auths") || !containsString(response.Readiness.Reasons, "missing_account_id") {
		t.Fatalf("unexpected readiness: %#v", response.Readiness)
	}
}

func TestEgressEndpointViewReportsExpectedObservedIPAndEligibility(t *testing.T) {
	handler, service := newEgressManagementHandler(t, nil)
	endpoint := createReadyEgressEndpoint(t, service, "17", "100.64.0.17", "198.51.100.17")

	recorder := invokeEgressHandler(t, http.MethodGet, "/v0/management/egress/endpoints", "", nil, handler.GetEgressEndpoints)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []struct {
			ID               string                   `json:"id"`
			ExpectedPublicIP string                   `json:"expected_public_ip"`
			ObservedPublicIP string                   `json:"observed_public_ip"`
			Eligibility      egress.EndpointReadiness `json:"eligibility"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].ID != endpoint.ID || response.Items[0].ExpectedPublicIP != "198.51.100.17" || response.Items[0].ObservedPublicIP != "198.51.100.17" || !response.Items[0].Eligibility.RuntimeReady {
		t.Fatalf("unexpected endpoint view: %#v body=%s", response.Items, recorder.Body.String())
	}
}

func TestEgressBindingBatchPreviewApplyIsConfirmedAtomicAndRevisioned(t *testing.T) {
	handler, service := newEgressManagementHandler(t, nil)
	e1 := createReadyEgressEndpoint(t, service, "17", "100.64.0.17", "198.51.100.17")
	e2 := createReadyEgressEndpoint(t, service, "18", "100.64.0.18", "198.51.100.18")
	if err := service.Store().UpsertNodes(context.Background(), []egress.Node{
		{ID: "17", Name: "17", Addresses: []string{"100.64.0.17"}, Online: true, Tags: []string{config.DefaultEgressServiceTag}},
		{ID: "18", Name: "18", Addresses: []string{"100.64.0.18"}, Online: true, Tags: []string{config.DefaultEgressServiceTag}},
	}, time.Now()); err != nil {
		t.Fatalf("refresh nodes: %v", err)
	}
	i1, _ := egress.StableIdentity("acct-one")
	i2, _ := egress.StableIdentity("acct-two")
	assignments := []egress.BindingAssignment{{Identity: i1, EndpointID: e1.ID, AuthFileID: "one.json"}, {Identity: i2, EndpointID: e2.ID, AuthFileID: "two.json"}}

	preview := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/bindings/preview", "", gin.H{"assignments": assignments}, handler.PostEgressBindingPreview)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	var plan egress.BindingBatchPreview
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || !plan.Valid || plan.Revision == "" {
		t.Fatalf("preview=%#v err=%v body=%s", plan, err, preview.Body.String())
	}

	unconfirmed := invokeEgressHandler(t, http.MethodPut, "/v0/management/egress/bindings/batch", "", gin.H{"revision": plan.Revision, "confirmed": false, "assignments": assignments}, handler.PutEgressBindingBatch)
	if unconfirmed.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed status=%d body=%s", unconfirmed.Code, unconfirmed.Body.String())
	}
	if bindings, _ := service.ListBindings(context.Background()); len(bindings) != 0 {
		t.Fatalf("unconfirmed request mutated bindings: %#v", bindings)
	}

	applied := invokeEgressHandler(t, http.MethodPut, "/v0/management/egress/bindings/batch", "", gin.H{"revision": plan.Revision, "confirmed": true, "assignments": assignments}, handler.PutEgressBindingBatch)
	if applied.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", applied.Code, applied.Body.String())
	}
	bindings, _ := service.ListBindings(context.Background())
	if len(bindings) != 2 {
		t.Fatalf("bindings=%#v", bindings)
	}

	stale := invokeEgressHandler(t, http.MethodPut, "/v0/management/egress/bindings/batch", "", gin.H{"revision": plan.Revision, "confirmed": true, "assignments": []egress.BindingAssignment{{Identity: i1, EndpointID: e2.ID}}}, handler.PutEgressBindingBatch)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"code":"egress_plan_stale"`) {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
	after, _ := service.ListBindings(context.Background())
	if len(after) != 2 || after[0].EndpointID != bindings[0].EndpointID || after[1].EndpointID != bindings[1].EndpointID {
		t.Fatalf("stale request partially mutated bindings: before=%#v after=%#v", bindings, after)
	}
}

func TestEgressBindingBatchCanPreviewAndApplyUnbind(t *testing.T) {
	handler, service := newEgressManagementHandler(t, nil)
	endpoint := createReadyEgressEndpoint(t, service, "17", "100.64.0.17", "198.51.100.17")
	identity, _ := egress.StableIdentity("acct-unbind")
	if err := service.PutBinding(context.Background(), egress.Binding{Identity: identity, EndpointID: endpoint.ID, AuthFileID: "unbind.json"}); err != nil {
		t.Fatal(err)
	}
	assignments := []egress.BindingAssignment{{Identity: identity, EndpointID: "", AuthFileID: "unbind.json"}}

	preview := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/bindings/preview", "", gin.H{"assignments": assignments}, handler.PostEgressBindingPreview)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	var plan egress.BindingBatchPreview
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || !plan.Valid || len(plan.Assignments) != 1 || plan.Assignments[0].EndpointID != "" {
		t.Fatalf("preview=%#v err=%v body=%s", plan, err, preview.Body.String())
	}
	applied := invokeEgressHandler(t, http.MethodPut, "/v0/management/egress/bindings/batch", "", gin.H{"revision": plan.Revision, "confirmed": true, "assignments": assignments}, handler.PutEgressBindingBatch)
	if applied.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", applied.Code, applied.Body.String())
	}
	bindings, err := service.ListBindings(context.Background())
	if err != nil || len(bindings) != 0 {
		t.Fatalf("bindings after unbind=%#v err=%v", bindings, err)
	}
}

func TestEgressBindingBatchRejectsEmptyIdentity(t *testing.T) {
	handler, _ := newEgressManagementHandler(t, nil)
	recorder := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/bindings/preview", "", gin.H{
		"assignments": []egress.BindingAssignment{{Identity: "", EndpointID: ""}},
	}, handler.PostEgressBindingPreview)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_identity"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestEgressEndpointImpactRequiresConfirmedDisableAndKeepsBoundDeleteProhibited(t *testing.T) {
	handler, service := newEgressManagementHandler(t, nil)
	endpoint := createReadyEgressEndpoint(t, service, "17", "100.64.0.17", "198.51.100.17")
	identity, _ := egress.StableIdentity("acct-bound")
	if err := service.PutBinding(context.Background(), egress.Binding{Identity: identity, EndpointID: endpoint.ID, AuthFileID: "bound.json"}); err != nil {
		t.Fatal(err)
	}

	impact := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/endpoints/"+endpoint.ID+"/impact", endpoint.ID, gin.H{"action": "disable"}, handler.PostEgressEndpointImpact)
	if impact.Code != http.StatusOK || !strings.Contains(impact.Body.String(), `"binding_count":1`) || !strings.Contains(impact.Body.String(), `"requires_confirmation":true`) {
		t.Fatalf("impact status=%d body=%s", impact.Code, impact.Body.String())
	}
	var impactBody struct {
		Revision string `json:"revision"`
	}
	_ = json.Unmarshal(impact.Body.Bytes(), &impactBody)

	rejected := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/endpoints/"+endpoint.ID+"/actions", endpoint.ID, gin.H{"action": "disable", "revision": impactBody.Revision, "confirmed": false}, handler.PostEgressEndpointAction)
	if rejected.Code != http.StatusConflict || !strings.Contains(rejected.Body.String(), `"code":"egress_confirmation_required"`) {
		t.Fatalf("unconfirmed disable status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	current, _ := service.GetEndpoint(context.Background(), endpoint.ID)
	if !current.Enabled {
		t.Fatal("unconfirmed disable changed endpoint")
	}

	disabled := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/endpoints/"+endpoint.ID+"/actions", endpoint.ID, gin.H{"action": "disable", "revision": impactBody.Revision, "confirmed": true}, handler.PostEgressEndpointAction)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	current, _ = service.GetEndpoint(context.Background(), endpoint.ID)
	if current.Enabled {
		t.Fatal("confirmed disable did not change endpoint")
	}

	deleteImpact := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/endpoints/"+endpoint.ID+"/impact", endpoint.ID, gin.H{"action": "delete"}, handler.PostEgressEndpointImpact)
	var deletePlan struct {
		Revision string `json:"revision"`
	}
	_ = json.Unmarshal(deleteImpact.Body.Bytes(), &deletePlan)
	deleted := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/endpoints/"+endpoint.ID+"/actions", endpoint.ID, gin.H{"action": "delete", "revision": deletePlan.Revision, "confirmed": true}, handler.PostEgressEndpointAction)
	if deleted.Code != http.StatusConflict {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func createReadyEgressEndpoint(t *testing.T, service *egress.Service, nodeID, host, publicIP string) egress.Endpoint {
	t.Helper()
	ctx := context.Background()
	if err := service.Store().UpsertNodes(ctx, []egress.Node{{ID: nodeID, Name: nodeID, Addresses: []string{host}, Online: true, Tags: []string{config.DefaultEgressServiceTag}}}, time.Now()); err != nil {
		t.Fatalf("UpsertNodes() error = %v", err)
	}
	endpoint, err := service.CreateEndpoint(ctx, egress.Endpoint{Name: nodeID, NodeID: nodeID, Protocol: egress.ProtocolSOCKS5, Host: host, Port: 1080, Enabled: true, ExpectedPublicIP: publicIP})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	endpoint, err = service.Store().UpdateEndpointCheck(ctx, endpoint.ID, publicIP, egress.EndpointStatusHealthy, "", 10, time.Now())
	if err != nil {
		t.Fatalf("UpdateEndpointCheck() error = %v", err)
	}
	return endpoint
}

func invokeEgressHandler(t *testing.T, method, path, id string, body any, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	handler(c)
	return recorder
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func newEgressManagementHandler(t *testing.T, manager *coreauth.Manager) (*Handler, *egress.Service) {
	t.Helper()
	cfg := &config.Config{EgressNetwork: config.EgressNetworkConfig{Enabled: true, Headscale: config.HeadscaleConfig{ServiceTag: config.DefaultEgressServiceTag}}}
	return newEgressManagementHandlerWithConfig(t, cfg, manager)
}

func newEgressManagementHandlerWithConfig(t *testing.T, cfg *config.Config, manager *coreauth.Manager) (*Handler, *egress.Service) {
	t.Helper()
	service, err := egress.NewService(cfg, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	handler := NewHandler(cfg, filepath.Join(t.TempDir(), "config.yaml"), manager)
	handler.SetEgressService(service)
	return handler, service
}
