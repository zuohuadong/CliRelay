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

func TestEgressOverviewUsesEndpointBindingContractOnly(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{ID: "bound.json", Provider: "codex", Metadata: map[string]any{"account_id": "acct-bound"}})
	handler, service := newEgressManagementHandler(t, manager)
	endpoint := createReadyEgressEndpoint(t, service, "10.77.0.2", "198.51.100.2")
	identity, _ := egress.StableIdentity("acct-bound")
	if err := service.PutBinding(context.Background(), egress.Binding{Identity: identity, EndpointID: endpoint.ID, AuthFileID: "bound.json"}); err != nil {
		t.Fatal(err)
	}

	recorder := invokeEgressHandler(t, http.MethodGet, "/v0/management/egress/overview", "", nil, handler.GetEgressOverview)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"headscale", "nodes", "local_endpoint", "enrollment"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("overview contains legacy %q: %s", forbidden, body)
		}
	}
	var response struct {
		Enabled bool           `json:"enabled"`
		Counts  map[string]int `json:"counts"`
		Policy  struct {
			BindingMode string `json:"binding_mode"`
			FailureMode string `json:"failure_mode"`
		} `json:"policy"`
		Readiness struct {
			CodexOAuthAllowed bool `json:"codex_oauth_allowed"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Enabled || response.Counts["endpoints"] != 1 || response.Counts["bindings"] != 1 || response.Policy.BindingMode != "per_endpoint" || response.Policy.FailureMode != "fail_closed" || !response.Readiness.CodexOAuthAllowed {
		t.Fatalf("overview = %#v", response)
	}
}

func TestEgressOverviewDisabledBlocksCodexOAuth(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{EgressNetwork: config.EgressNetworkConfig{Enabled: false}}
	handler, _ := newEgressManagementHandlerWithConfig(t, cfg, nil)
	recorder := invokeEgressHandler(t, http.MethodGet, "/v0/management/egress/overview", "", nil, handler.GetEgressOverview)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"enabled":false`) || !strings.Contains(recorder.Body.String(), `"codex_oauth_allowed":false`) || !strings.Contains(recorder.Body.String(), `"code":"runtime_disabled"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestEgressOverviewAllowsCodexOAuthRecoveryWithGlobalReadinessBlockers(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{ID: "missing.json", Provider: "codex", Metadata: map[string]any{}})
	_, _ = manager.Register(context.Background(), &coreauth.Auth{ID: "unbound.json", Provider: "codex", Metadata: map[string]any{"account_id": "acct-unbound"}})
	_, _ = manager.Register(context.Background(), &coreauth.Auth{ID: "unhealthy.json", Provider: "codex", Metadata: map[string]any{"account_id": "acct-unhealthy"}})
	handler, service := newEgressManagementHandler(t, manager)
	_ = createReadyEgressEndpoint(t, service, "10.77.0.2", "198.51.100.2")
	unhealthy := createReadyEgressEndpoint(t, service, "10.77.0.3", "198.51.100.3")
	identity, _ := egress.StableIdentity("acct-unhealthy")
	if err := service.PutBinding(context.Background(), egress.Binding{Identity: identity, EndpointID: unhealthy.ID, AuthFileID: "unhealthy.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store().UpdateEndpointCheck(context.Background(), unhealthy.ID, "", egress.EndpointStatusUnhealthy, "proxy unavailable", 0, time.Now()); err != nil {
		t.Fatal(err)
	}

	recorder := invokeEgressHandler(t, http.MethodGet, "/v0/management/egress/overview", "", nil, handler.GetEgressOverview)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Readiness struct {
			ReadyToEnable     bool     `json:"ready_to_enable"`
			CodexOAuthAllowed bool     `json:"codex_oauth_allowed"`
			Reasons           []string `json:"reasons"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	reasons := make(map[string]bool, len(response.Readiness.Reasons))
	for _, reason := range response.Readiness.Reasons {
		reasons[reason] = true
	}
	if response.Readiness.ReadyToEnable || !response.Readiness.CodexOAuthAllowed || !reasons["missing_account_id"] || !reasons["unbound_codex_auths"] || !reasons["bound_endpoint_not_ready"] {
		t.Fatalf("readiness = %#v", response.Readiness)
	}
}

func TestEgressEndpointsResponseNeverReturnsPasswordOrLegacyFields(t *testing.T) {
	t.Parallel()

	handler, service := newEgressManagementHandler(t, nil)
	endpoint, err := service.CreateEndpoint(context.Background(), egress.Endpoint{
		Name: "sg socks", Protocol: egress.ProtocolSOCKS5, Host: "10.77.0.2", Port: 1080, Enabled: true,
		Username: "relay", Password: "super-secret", ExpectedPublicIP: "198.51.100.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = service.Store().UpdateEndpointCheck(context.Background(), endpoint.ID, endpoint.ExpectedPublicIP, egress.EndpointStatusHealthy, "", 1, time.Now())
	recorder := invokeEgressHandler(t, http.MethodGet, "/v0/management/egress/endpoints", "", nil, handler.GetEgressEndpoints)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "super-secret") || strings.Contains(body, `"password"`) || strings.Contains(body, `"node_id"`) || strings.Contains(body, `"local_server"`) {
		t.Fatalf("endpoint response leaked legacy or secret fields: %s", body)
	}
	if !strings.Contains(body, `"has_credentials":true`) || !strings.Contains(body, `"runtime_ready":true`) {
		t.Fatalf("endpoint response missing readiness: %s", body)
	}
}

func TestEgressBindingsIncludesUnboundAndMissingIdentityCodexAuth(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{ID: "codex-user.json", Provider: "codex", Metadata: map[string]any{"account_id": "acct-123"}})
	_, _ = manager.Register(context.Background(), &coreauth.Auth{ID: "codex-missing.json", Provider: "codex", Metadata: map[string]any{}})
	handler, _ := newEgressManagementHandler(t, manager)
	recorder := invokeEgressHandler(t, http.MethodGet, "/v0/management/egress/bindings", "", nil, handler.GetEgressBindings)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"auth_id":"codex-user.json"`) || !strings.Contains(recorder.Body.String(), `"bound":false`) || !strings.Contains(recorder.Body.String(), "missing account_id") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestEgressBindingBatchPreviewApplyIsConfirmedAndRevisioned(t *testing.T) {
	t.Parallel()

	handler, service := newEgressManagementHandler(t, nil)
	e1 := createReadyEgressEndpoint(t, service, "10.77.0.2", "198.51.100.2")
	e2 := createReadyEgressEndpoint(t, service, "10.77.0.3", "198.51.100.3")
	i1, _ := egress.StableIdentity("acct-one")
	i2, _ := egress.StableIdentity("acct-two")
	assignments := []egress.BindingAssignment{{Identity: i1, EndpointID: e1.ID, AuthFileID: "one.json"}, {Identity: i2, EndpointID: e2.ID, AuthFileID: "two.json"}}

	preview := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/bindings/preview", "", gin.H{"assignments": assignments}, handler.PostEgressBindingPreview)
	var plan egress.BindingBatchPreview
	if preview.Code != http.StatusOK || json.Unmarshal(preview.Body.Bytes(), &plan) != nil || !plan.Valid || plan.Revision == "" {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	unconfirmed := invokeEgressHandler(t, http.MethodPut, "/v0/management/egress/bindings/batch", "", gin.H{"revision": plan.Revision, "confirmed": false, "assignments": assignments}, handler.PutEgressBindingBatch)
	if unconfirmed.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed status=%d body=%s", unconfirmed.Code, unconfirmed.Body.String())
	}
	applied := invokeEgressHandler(t, http.MethodPut, "/v0/management/egress/bindings/batch", "", gin.H{"revision": plan.Revision, "confirmed": true, "assignments": assignments}, handler.PutEgressBindingBatch)
	if applied.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", applied.Code, applied.Body.String())
	}
	stale := invokeEgressHandler(t, http.MethodPut, "/v0/management/egress/bindings/batch", "", gin.H{"revision": plan.Revision, "confirmed": true, "assignments": []egress.BindingAssignment{{Identity: i1, EndpointID: e2.ID}}}, handler.PutEgressBindingBatch)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"code":"egress_plan_stale"`) {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
}

func TestEgressEndpointImpactRequiresConfirmedDisableAndProtectsBoundDelete(t *testing.T) {
	t.Parallel()

	handler, service := newEgressManagementHandler(t, nil)
	endpoint := createReadyEgressEndpoint(t, service, "10.77.0.2", "198.51.100.2")
	identity, _ := egress.StableIdentity("acct-bound")
	if err := service.PutBinding(context.Background(), egress.Binding{Identity: identity, EndpointID: endpoint.ID}); err != nil {
		t.Fatal(err)
	}
	impact := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/endpoints/"+endpoint.ID+"/impact", endpoint.ID, gin.H{"action": "disable"}, handler.PostEgressEndpointImpact)
	var plan struct {
		Revision string `json:"revision"`
	}
	_ = json.Unmarshal(impact.Body.Bytes(), &plan)
	if impact.Code != http.StatusOK || plan.Revision == "" || !strings.Contains(impact.Body.String(), `"binding_count":1`) {
		t.Fatalf("impact status=%d body=%s", impact.Code, impact.Body.String())
	}
	rejected := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/endpoints/"+endpoint.ID+"/actions", endpoint.ID, gin.H{"action": "disable", "revision": plan.Revision, "confirmed": false}, handler.PostEgressEndpointAction)
	if rejected.Code != http.StatusConflict {
		t.Fatalf("unconfirmed status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	deleteImpact := invokeEgressHandler(t, http.MethodPost, "/v0/management/egress/endpoints/"+endpoint.ID+"/impact", endpoint.ID, gin.H{"action": "delete"}, handler.PostEgressEndpointImpact)
	if deleteImpact.Code != http.StatusOK || !strings.Contains(deleteImpact.Body.String(), `"allowed":false`) || !strings.Contains(deleteImpact.Body.String(), "endpoint_has_bindings") {
		t.Fatalf("delete impact status=%d body=%s", deleteImpact.Code, deleteImpact.Body.String())
	}
}

func createReadyEgressEndpoint(t *testing.T, service *egress.Service, host, publicIP string) egress.Endpoint {
	t.Helper()
	endpoint, err := service.CreateEndpoint(context.Background(), egress.Endpoint{
		Name: host, Protocol: egress.ProtocolSOCKS5, Host: host, Port: 1080, Enabled: true, ExpectedPublicIP: publicIP,
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}
	endpoint, err = service.Store().UpdateEndpointCheck(context.Background(), endpoint.ID, publicIP, egress.EndpointStatusHealthy, "", 10, time.Now())
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

func newEgressManagementHandler(t *testing.T, manager *coreauth.Manager) (*Handler, *egress.Service) {
	t.Helper()
	return newEgressManagementHandlerWithConfig(t, &config.Config{EgressNetwork: config.EgressNetworkConfig{Enabled: true}}, manager)
}

func newEgressManagementHandlerWithConfig(t *testing.T, cfg *config.Config, manager *coreauth.Manager) (*Handler, *egress.Service) {
	t.Helper()
	service, err := egress.NewService(cfg, filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	handler := NewHandler(cfg, filepath.Join(t.TempDir(), "config.yaml"), manager)
	handler.SetEgressService(service)
	return handler, service
}
