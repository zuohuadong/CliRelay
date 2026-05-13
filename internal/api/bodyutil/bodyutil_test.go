package bodyutil

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLimitBodyMiddlewareRejectsOversizedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(LimitBodyMiddleware(8))
	r.POST("/management", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/management", bytes.NewReader([]byte(`{"value":"too-large"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, w.Code)
	}
}

func TestLimitBodyMiddlewareRestoresBodyForHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(LimitBodyMiddleware(64))
	r.POST("/management", func(c *gin.Context) {
		body, err := ReadRequestBody(c, 64)
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
		c.String(http.StatusOK, string(body))
	})

	req := httptest.NewRequest(http.MethodPost, "/management", bytes.NewReader([]byte(`{"value":"ok"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
	if got := w.Body.String(); got != `{"value":"ok"}` {
		t.Fatalf("unexpected response body: %s", got)
	}
}
