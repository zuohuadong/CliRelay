package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPutBillingMultipliersNormalizesAndPersists(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/billing-multipliers", strings.NewReader(`{"value":{" Antigravity ":0.2}}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutBillingMultipliers(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.BillingMultipliers["antigravity"]; got != 0.2 {
		t.Fatalf("billing multiplier = %v, want 0.2", got)
	}
}

func TestPutBillingMultipliersRejectsNonPositiveValues(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/billing-multipliers", strings.NewReader(`{"value":{"antigravity":0}}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutBillingMultipliers(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(h.cfg.BillingMultipliers) != 0 {
		t.Fatalf("billing multipliers changed: %#v", h.cfg.BillingMultipliers)
	}
}
