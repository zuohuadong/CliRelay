package management

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestUploadAuthFile_PreservesPriorityAttributes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	content := `{"type":"codex","email":"midai0530@gmail.com","priority":98}`

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "codex-midai0530@gmail.com-plus.json")
	if err != nil {
		t.Fatalf("failed to create multipart file: %v", err)
	}
	if _, err = part.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write multipart content: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if status, _ := payload["status"].(string); status != "ok" {
		t.Fatalf("expected status ok, got %#v", payload["status"])
	}

	auth, ok := manager.GetByID("codex-midai0530@gmail.com-plus.json")
	if !ok || auth == nil {
		t.Fatalf("expected uploaded auth record to exist")
	}
	if got := auth.Attributes["priority"]; got != "98" {
		t.Fatalf("priority attribute = %q, want %q", got, "98")
	}
	if got := auth.Metadata["priority"]; got != float64(98) {
		t.Fatalf("priority metadata = %#v, want 98", got)
	}
}

func TestBackfillCodexAccountIDInDataPersistsJWTAccountID(t *testing.T) {
	idToken := "eyJhbGciOiAiUlMyNTYiLCAidHlwIjogIkpXVCJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOiB7ImNoYXRncHRfYWNjb3VudF9pZCI6ICJhY2N0LWp3dC1iYWNrZmlsbCIsICJjaGF0Z3B0X3BsYW5fdHlwZSI6ICJwbHVzIn19.ZmFrZXNpZw"

	// No top-level account_id but carries a valid id_token.
	data := []byte(`{"type":"codex","email":"user@example.test","id_token":"` + idToken + `"}`)
	backfilled, changed := backfillCodexAccountIDInData(data)
	if !changed {
		t.Fatal("backfillCodexAccountIDInData() changed = false, want true")
	}
	var persisted map[string]any
	if err := json.Unmarshal(backfilled, &persisted); err != nil {
		t.Fatalf("unmarshal backfilled: %v", err)
	}
	if got, _ := persisted["account_id"].(string); got != "acct-jwt-backfill" {
		t.Fatalf("persisted account_id = %q, want acct-jwt-backfill", got)
	}
	// Non-codex auths are left untouched.
	other := []byte(`{"type":"gemini","api_key":"x"}`)
	if _, changed := backfillCodexAccountIDInData(other); changed {
		t.Fatal("backfill mutated a non-codex auth")
	}
	// Auths that already have account_id are left untouched.
	existing := []byte(`{"type":"codex","account_id":"acct-existing","id_token":"` + idToken + `"}`)
	if _, changed := backfillCodexAccountIDInData(existing); changed {
		t.Fatal("backfill mutated an auth that already had account_id")
	}
}

func TestUploadAuthFileBindsCodexAuthToEgressOnImport(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir, EgressNetwork: config.EgressNetworkConfig{Enabled: true}}
	manager := coreauth.NewManager(nil, nil, nil)
	handler := NewHandler(cfg, filepath.Join(t.TempDir(), "config.yaml"), manager)

	service, err := egress.NewService(cfg, filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	handler.SetEgressService(service)

	endpoint := createReadyEgressEndpoint(t, service, "10.77.0.2", "198.51.100.2")

	// Import a codex auth via the body path and request binding to the endpoint.
	body := `{"type":"codex","email":"user@example.test","account_id":"acct-import","access_token":"tok"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files?name=codex-import.json&egress_id="+endpoint.ID, strings.NewReader(body))
	ctx.Request = req

	handler.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"bound_egress":"`+endpoint.ID+`"`) {
		t.Fatalf("response missing bound_egress: %s", rec.Body.String())
	}

	// Verify the binding was persisted.
	identity, err := egress.StableIdentity("acct-import")
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := service.ListBindings(context.Background())
	if err != nil {
		t.Fatalf("ListBindings() error = %v", err)
	}
	var found bool
	for _, b := range bindings {
		if b.Identity == identity && b.EndpointID == endpoint.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("binding not persisted for identity %s: %+v", identity, bindings)
	}
}

func TestUploadAuthFileBindsCodexAuthFromIDTokenOnImport(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir, EgressNetwork: config.EgressNetworkConfig{Enabled: true}}
	manager := coreauth.NewManager(nil, nil, nil)
	handler := NewHandler(cfg, filepath.Join(t.TempDir(), "config.yaml"), manager)

	service, err := egress.NewService(cfg, filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	handler.SetEgressService(service)

	endpoint := createReadyEgressEndpoint(t, service, "10.77.0.3", "198.51.100.3")

	// Codex auth with no top-level account_id but a valid id_token; the import
	// must derive account_id from the JWT and bind successfully.
	idToken := "eyJhbGciOiAiUlMyNTYiLCAidHlwIjogIkpXVCJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOiB7ImNoYXRncHRfYWNjb3VudF9pZCI6ICJhY2N0LWp3dC1iYWNrZmlsbCIsICJjaGF0Z3B0X3BsYW5fdHlwZSI6ICJwbHVzIn19.ZmFrZXNpZw"
	body := `{"type":"codex","email":"user@example.test","id_token":"` + idToken + `"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files?name=codex-jwt-import.json&egress_id="+endpoint.ID, strings.NewReader(body))
	ctx.Request = req

	handler.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "binding_error") {
		t.Fatalf("unexpected binding_error: %s", rec.Body.String())
	}

	identity, err := egress.StableIdentity("acct-jwt-backfill")
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := service.ListBindings(context.Background())
	if err != nil {
		t.Fatalf("ListBindings() error = %v", err)
	}
	var found bool
	for _, b := range bindings {
		if b.Identity == identity && b.EndpointID == endpoint.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("JWT-derived binding not persisted: %+v", bindings)
	}
}

func TestUploadAuthFileBindingFailsGracefullyForNonReadyEndpoint(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir, EgressNetwork: config.EgressNetworkConfig{Enabled: true}}
	manager := coreauth.NewManager(nil, nil, nil)
	handler := NewHandler(cfg, filepath.Join(t.TempDir(), "config.yaml"), manager)

	service, err := egress.NewService(cfg, filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	handler.SetEgressService(service)

	// Endpoint exists but is never marked healthy, so it is not runtime ready.
	unhealthy, err := service.CreateEndpoint(context.Background(), egress.Endpoint{
		Name: "unhealthy", Protocol: egress.ProtocolSOCKS5, Host: "10.77.0.9", Port: 1080, Enabled: true, ExpectedPublicIP: "203.0.113.9",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint() error = %v", err)
	}

	body := `{"type":"codex","email":"user@example.test","account_id":"acct-import","access_token":"tok"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files?name=codex-fail.json&egress_id="+unhealthy.ID, strings.NewReader(body))
	ctx.Request = req

	handler.UploadAuthFile(ctx)

	// Import must still succeed; only binding reports an error.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "binding_error") {
		t.Fatalf("expected binding_error for non-ready endpoint: %s", rec.Body.String())
	}
	// Auth file must still be on disk despite binding failure.
	if _, statErr := os.Stat(filepath.Join(authDir, "codex-fail.json")); statErr != nil {
		t.Fatalf("auth file not persisted despite binding failure: %v", statErr)
	}
}
