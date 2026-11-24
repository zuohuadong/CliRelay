package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRequestContextOrBackgroundUsesRequestContextWhenPresent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	reqCtx, cancel := context.WithCancel(req.Context())
	c.Request = req.WithContext(reqCtx)

	ctx := requestContextOrBackground(c)
	cancel()

	select {
	case <-ctx.Done():
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected requestContextOrBackground to return the request context when present")
	}
}

func TestRequestContextOrBackgroundFallsBackWhenRequestMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	ctx := requestContextOrBackground(c)
	if ctx == nil {
		t.Fatal("expected non-nil fallback context")
	}

	select {
	case <-ctx.Done():
		t.Fatal("fallback context should not be canceled")
	default:
	}
}
