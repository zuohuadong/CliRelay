package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAPIKeyConcurrencyMiddlewareRejectsAtLimit(t *testing.T) {
	concurrencyCounters = sync.Map{}
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("accessMetadata", map[string]string{"concurrency-limit": "1"})
		c.Set("userApiKey", "sk-test")
		c.Next()
	})
	router.Use(APIKeyConcurrencyMiddleware())
	router.GET("/blocked", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	counterVal, _ := concurrencyCounters.LoadOrStore("sk-test", &concurrencyCounter{})
	counter := counterVal.(*concurrencyCounter)
	counter.current.Store(1)

	req := httptest.NewRequest(http.MethodGet, "/blocked", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if got := counter.current.Load(); got != 1 {
		t.Fatalf("counter changed after rejected request: %d", got)
	}
}

func TestAPIKeySystemPromptMiddlewareRestoresBodyWhenUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("accessMetadata", map[string]string{"system-prompt": "Use workspace rules."})
		c.Next()
	})
	router.Use(APIKeySystemPromptMiddleware())
	router.POST("/responses", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		c.String(http.StatusOK, string(body))
	})

	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Body.String() != `{"input":"hello"}` {
		t.Fatalf("body = %q, want original body", rec.Body.String())
	}
}
