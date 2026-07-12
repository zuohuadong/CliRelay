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

func TestAPIKeySystemPromptMiddlewareUpdatesCanonicalBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("accessMetadata", map[string]string{"system-prompt": "Use workspace rules."})
		SetCanonicalResponsesBody(c, []byte(`{"messages":[{"role":"user","content":"hello"}]}`))
		c.Next()
	})
	router.Use(APIKeySystemPromptMiddleware())
	router.POST("/v1/responses", func(c *gin.Context) {
		body, ok := CanonicalResponsesBody(c)
		if !ok || !strings.Contains(string(body), "Use workspace rules.") {
			t.Fatalf("canonical body = %q, ok=%v", body, ok)
		}
		c.Status(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"ignored":true}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestExtractModelFromRequestUsesCanonicalBody(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	SetCanonicalResponsesBody(c, []byte(`{"model":"gpt-canonical"}`))
	c.Request.Body = &failReadCloser{t: t}
	if got := extractModelFromRequest(c); got != "gpt-canonical" {
		t.Fatalf("model = %q", got)
	}
}

func TestAPIKeySystemPromptMiddlewareResizesCanonicalReservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := NewResponsesIngressController(ResponsesIngressConfig{MaxInboundBytes: 96, MemoryBudgetBytes: 384})
	router := gin.New()
	router.Use(controller.Middleware())
	router.Use(func(c *gin.Context) {
		c.Set("accessMetadata", map[string]string{"system-prompt": strings.Repeat("p", 80)})
		c.Next()
	})
	router.Use(APIKeySystemPromptMiddleware())
	var reached bool
	router.POST("/v1/responses", func(c *gin.Context) { reached = true; c.Status(http.StatusNoContent) })

	body := `{"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
	if reached {
		t.Fatal("handler ran after system prompt expanded canonical body over hard max")
	}
}

func TestAPIKeySystemPromptMiddlewareRejectsCanonicalAggregateGrowth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := NewResponsesIngressController(ResponsesIngressConfig{MaxInboundBytes: 1024, MemoryBudgetBytes: 220})
	router := gin.New()
	router.Use(controller.Middleware())
	router.Use(func(c *gin.Context) {
		c.Set("accessMetadata", map[string]string{"system-prompt": strings.Repeat("p", 40)})
		c.Next()
	})
	router.Use(APIKeySystemPromptMiddleware())
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	body := `{"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") != "1" || !strings.Contains(rec.Body.String(), "request_memory_budget_exceeded") {
		t.Fatalf("headers/body = %v %s", rec.Header(), rec.Body.String())
	}
	if got := controller.InUseBytes(); got != 0 {
		t.Fatalf("budget in use after rejection = %d", got)
	}
}
