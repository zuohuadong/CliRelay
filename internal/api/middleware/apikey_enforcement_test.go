package middleware

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	_ "modernc.org/sqlite"
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

func TestAPIKeyQuotaMiddlewareRejectsMonthlySpendingLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	usage.RegisterSQLiteSink(dbPath)
	now := time.Now().UTC()
	usage.APIKeyUsageBetween("sk-monthly", now.Add(-2*time.Hour), now)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`insert into request_logs (timestamp, api_key, failed, total_tokens, cost) values (?, ?, 0, 100, 1.25)`,
		now.Add(-1*time.Hour).Format(time.RFC3339Nano), "sk-monthly")
	if err != nil {
		t.Fatalf("insert request log: %v", err)
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("accessMetadata", map[string]string{
			"monthly-spending-limit": "1.0",
			"billing-cycle-anchor":   now.Add(-24 * time.Hour).Format(time.RFC3339),
		})
		c.Set("userApiKey", "sk-monthly")
		c.Next()
	})
	router.Use(APIKeyQuotaMiddleware())
	router.GET("/blocked", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/blocked", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusTooManyRequests, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "monthly_spending_limit_exceeded") {
		t.Fatalf("body = %q, want monthly_spending_limit_exceeded", rec.Body.String())
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
