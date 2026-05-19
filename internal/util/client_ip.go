package util

import (
	"net"
	"strings"

	"github.com/gin-gonic/gin"
)

// RealClientIP returns the effective client IP for requests that may be
// forwarded through a trusted loopback reverse proxy.
func RealClientIP(c *gin.Context) string {
	if c == nil {
		return ""
	}

	clientIP := strings.TrimSpace(c.ClientIP())
	req := c.Request
	if req == nil {
		return clientIP
	}

	host := strings.TrimSpace(req.RemoteAddr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	remoteIP := net.ParseIP(host)
	if remoteIP != nil {
		clientIP = remoteIP.String()
	}
	if remoteIP == nil || !remoteIP.IsLoopback() {
		return clientIP
	}

	if ip := parseForwardedIP(req.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}

	for _, part := range strings.Split(req.Header.Get("X-Forwarded-For"), ",") {
		if ip := parseForwardedIP(part); ip != "" {
			return ip
		}
	}

	return clientIP
}

func parseForwardedIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if ip := net.ParseIP(raw); ip != nil {
		return ip.String()
	}
	return ""
}
