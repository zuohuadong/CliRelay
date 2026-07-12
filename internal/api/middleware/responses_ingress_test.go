package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

func TestResponsesIngressControllerCanonicalizesBeforeDownstreamReaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := NewResponsesIngressController(ResponsesIngressConfig{MaxInboundBytes: 1024, MemoryBudgetBytes: 4096})
	router := gin.New()
	router.Use(controller.Middleware())
	router.Use(func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Fatalf("downstream read error = %v", err)
		}
		if string(body) != `{"model":"gpt-test"}` {
			t.Fatalf("downstream body = %q", body)
		}
		if got := c.Request.Header.Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want cleared", got)
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Next()
	})
	router.POST("/v1/responses", func(c *gin.Context) {
		body, ok := CanonicalResponsesBody(c)
		if !ok || string(body) != `{"model":"gpt-test"}` {
			t.Fatalf("canonical body = %q, ok=%v", body, ok)
		}
		c.Status(http.StatusNoContent)
	})

	var encoded bytes.Buffer
	zw, err := zstd.NewWriter(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = zw.Write([]byte(`{"model":"gpt-test"}`))
	_ = zw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(encoded.Bytes()))
	req.Header.Set("Content-Encoding", "zstd")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := controller.InUseBytes(); got != 0 {
		t.Fatalf("budget in use after request = %d", got)
	}
}

func TestResponsesIngressControllerRejectsAggregateBudgetAndReleasesReservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := NewResponsesIngressController(ResponsesIngressConfig{MaxInboundBytes: 16, MemoryBudgetBytes: 64})
	entered := make(chan struct{})
	release := make(chan struct{})
	router := gin.New()
	router.Use(controller.Middleware())
	router.POST("/v1/responses", func(c *gin.Context) {
		close(entered)
		<-release
		c.Status(http.StatusNoContent)
	})

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(make([]byte, 16)))
		router.ServeHTTP(rec, req)
		firstDone <- rec
	}()
	<-entered

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte("x")))
	req.ContentLength = -1
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("aggregate rejection status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("request_memory_budget_exceeded")) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	close(release)
	if got := (<-firstDone).Code; got != http.StatusNoContent {
		t.Fatalf("first status = %d", got)
	}
	if got := controller.InUseBytes(); got != 0 {
		t.Fatalf("budget in use after release = %d", got)
	}
}

func TestResponsesIngressControllerRejectsOversizedBeforeDownstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := NewResponsesIngressController(ResponsesIngressConfig{MaxInboundBytes: 8, MemoryBudgetBytes: 128})
	var downstream atomic.Bool
	router := gin.New()
	router.Use(controller.Middleware())
	router.Use(func(c *gin.Context) { downstream.Store(true); c.Next() })
	router.POST("/backend-api/codex/responses/compact", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/backend-api/codex/responses/compact", bytes.NewReader([]byte("123456789")))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if downstream.Load() {
		t.Fatal("downstream middleware ran after oversized ingress")
	}
}

func TestResponsesIngressControllerKnownOversizedReturns413BeforeBudgetCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := NewResponsesIngressController(ResponsesIngressConfig{MaxInboundBytes: 8, MemoryBudgetBytes: 16})
	router := gin.New()
	router.Use(controller.Middleware())
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(bytes.Repeat([]byte("x"), 1024)))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
}

func TestResponsesIngressControllerAcceptsMislabeledJSONZstdBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := NewResponsesIngressController(ResponsesIngressConfig{MaxInboundBytes: 1024, MemoryBudgetBytes: 4096})
	router := gin.New()
	router.Use(controller.Middleware())
	router.POST("/v1/responses", func(c *gin.Context) {
		body, _ := CanonicalResponsesBody(c)
		c.String(http.StatusOK, string(body))
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Content-Encoding", "zstd")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"model":"gpt-test"}` {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestResponsesIngressControllerResizesUnderdeclaredReservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := NewResponsesIngressController(ResponsesIngressConfig{MaxInboundBytes: 16, MemoryBudgetBytes: 16})
	router := gin.New()
	router.Use(controller.Middleware())
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte("12345678")))
	req.ContentLength = 1
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 after reservation resize; body=%s", rec.Code, rec.Body.String())
	}
	if got := controller.InUseBytes(); got != 0 {
		t.Fatalf("budget in use after resized rejection = %d", got)
	}
}
