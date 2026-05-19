package util

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRealClientIPUsesLoopbackProxyHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Real-IP", "203.0.113.10")
	c.Request = req

	if got := RealClientIP(c); got != "203.0.113.10" {
		t.Fatalf("RealClientIP = %q, want %q", got, "203.0.113.10")
	}
}

func TestRealClientIPFallsBackToForwardedForWhenRealIPMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "[::1]:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.8, 127.0.0.1")
	c.Request = req

	if got := RealClientIP(c); got != "198.51.100.8" {
		t.Fatalf("RealClientIP = %q, want %q", got, "198.51.100.8")
	}
}

func TestRealClientIPIgnoresSpoofedProxyHeadersFromRemotePeer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.20:45678"
	req.Header.Set("X-Real-IP", "198.51.100.8")
	req.Header.Set("X-Forwarded-For", "198.51.100.8")
	c.Request = req

	if got := RealClientIP(c); got != "203.0.113.20" {
		t.Fatalf("RealClientIP = %q, want %q", got, "203.0.113.20")
	}
}
