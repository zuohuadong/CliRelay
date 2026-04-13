package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPublicLookupMiddlewareAppliesNoStoreHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.Use(publicLookupNoStoreMiddleware(), server.publicLookupRateLimitMiddleware())
	router.POST("/v0/management/public/test", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v0/management/public/test", nil)
	req.RemoteAddr = "198.51.100.10:1234"
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store, private, max-age=0" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q", got)
	}
	if got := rec.Header().Get("Expires"); got != "0" {
		t.Fatalf("Expires = %q", got)
	}
}

func TestPublicLookupRateLimitMiddlewareRejectsBurst(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.Use(publicLookupNoStoreMiddleware(), server.publicLookupRateLimitMiddleware())
	router.POST("/v0/management/public/test", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	for i := 0; i < publicLookupRateLimitMaxRequests; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v0/management/public/test", nil)
		req.RemoteAddr = "198.51.100.20:4321"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want %d", i+1, rec.Code, http.StatusNoContent)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/v0/management/public/test", nil)
	req.RemoteAddr = "198.51.100.20:4321"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("Retry-After header missing")
	}
}
