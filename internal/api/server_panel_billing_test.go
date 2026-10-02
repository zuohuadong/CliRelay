package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPanelBillingMultiplierRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: {port: 8317}\nmanagement: {secret-key: fixture-password}\nbilling-multipliers: {codex: 0.5}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := management.NewHandler(cfg, path, nil)
	h.SetLocalPassword("fixture-password")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()
	request := func(method, body string, authorized bool, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, "/v0/management/billing-multipliers", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Content-Type", "application/json")
		if authorized {
			req.Header.Set("Authorization", "Bearer fixture-password")
		}
		recorder := httptest.NewRecorder()
		s.engine.ServeHTTP(recorder, req)
		if recorder.Code != want {
			t.Fatalf("panel billing route %s: status=%d want=%d body=%s", method, recorder.Code, want, recorder.Body.String())
		}
		return strings.TrimSpace(recorder.Body.String())
	}
	if got := request(http.MethodGet, "", true, http.StatusOK); got != `{"billing-multipliers":{"codex":0.5}}` {
		t.Fatalf("initial billing multipliers = %s", got)
	}
	request(http.MethodGet, "", false, http.StatusUnauthorized)
	request(http.MethodPut, `{"value":{"codex":0.9}}`, false, http.StatusUnauthorized)
	request(http.MethodPut, `{"value":{"codex":0}}`, true, http.StatusBadRequest)
	if cfg.BillingMultipliers["codex"] != 0.5 {
		t.Fatal("rejected write changed billing settings")
	}
	request(http.MethodPut, `{"value":{" CODEX ":0.25}}`, true, http.StatusOK)
	if got := request(http.MethodGet, "", true, http.StatusOK); got != `{"billing-multipliers":{"codex":0.25}}` {
		t.Fatalf("updated billing multipliers = %s", got)
	}
	reloaded, err := config.LoadConfig(path)
	if err != nil || reloaded.BillingMultipliers["codex"] != 0.25 {
		t.Fatalf("billing multipliers not persisted: %v", err)
	}
	request(http.MethodPut, `{"value":{}}`, true, http.StatusOK)
	if got := request(http.MethodGet, "", true, http.StatusOK); got != `{"billing-multipliers":{}}` {
		t.Fatalf("cleared billing multipliers = %s", got)
	}
	s.managementRoutesEnabled.Store(false)
	request(http.MethodGet, "", true, http.StatusNotFound)
	request(http.MethodPut, `{"value":{"codex":0.8}}`, true, http.StatusNotFound)
}
