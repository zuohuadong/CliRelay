package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newTestServerWithOptions(t *testing.T, options ...ServerOption) *Server {
	t.Helper()
	return newTestServerWithConfig(t, nil, options...)
}

func setPanelSecurityHeaders(c *gin.Context) {
	c.Header("X-Frame-Options", "SAMEORIGIN")
	c.Header("Content-Security-Policy", "frame-ancestors 'self'")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "no-referrer")
}

func clearServerWriteDeadline(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})
}

func assertCodexNullableCatalogField(t *testing.T, entry map[string]any, field string) {
	t.Helper()
	if _, ok := entry[field]; !ok {
		t.Fatalf("catalog entry missing nullable field %q", field)
	}
}
