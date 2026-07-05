package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/videotool"
)

func (s *Server) nativeMCP(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if c.Request.Method == http.MethodOptions {
		c.Status(http.StatusNoContent)
		return
	}
	if c.Request.Method != http.MethodPost {
		c.Header("Allow", "POST, OPTIONS")
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "MCP endpoint accepts JSON-RPC POST requests"})
		return
	}

	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 8<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read MCP request"})
		return
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty MCP request"})
		return
	}

	server := &videotool.MCPServer{
		Client: videotool.NewClient(videotool.Options{
			BaseURL:    nativeMCPBaseURL(c),
			APIKey:     nativeMCPAPIKey(c),
			Model:      videotool.DefaultModel,
			HTTPClient: http.DefaultClient,
		}),
		RemoteHTTP: true,
	}
	resp, ok := server.HandleJSONRPC(c.Request.Context(), raw)
	if !ok {
		c.Status(http.StatusAccepted)
		return
	}
	c.Header("Content-Type", "application/json")
	c.Header("Mcp-Protocol-Version", firstNonEmptyNativeMCP(c.GetHeader("Mcp-Protocol-Version"), "2025-06-18"))
	if err := json.NewEncoder(c.Writer).Encode(resp); err != nil {
		return
	}
}

func nativeMCPBaseURL(c *gin.Context) string {
	proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto"))
	if proto == "" {
		proto = strings.TrimSpace(c.GetHeader("X-Forwarded-Protocol"))
	}
	if proto == "" && c.Request != nil && c.Request.TLS != nil {
		proto = "https"
	}
	if proto == "" {
		proto = "http"
	}
	if comma := strings.Index(proto, ","); comma >= 0 {
		proto = strings.TrimSpace(proto[:comma])
	}

	host := strings.TrimSpace(c.GetHeader("X-Forwarded-Host"))
	if host == "" && c.Request != nil {
		host = strings.TrimSpace(c.Request.Host)
	}
	if comma := strings.Index(host, ","); comma >= 0 {
		host = strings.TrimSpace(host[:comma])
	}
	if host == "" {
		host = "127.0.0.1:8317"
	}
	return proto + "://" + host
}

func nativeMCPAPIKey(c *gin.Context) string {
	if value, ok := c.Get("userApiKey"); ok {
		if key, ok := value.(string); ok && strings.TrimSpace(key) != "" {
			return strings.TrimSpace(key)
		}
	}
	return extractNativeMCPBearerToken(c.GetHeader("Authorization"))
}

func extractNativeMCPBearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "bearer") {
		return strings.TrimSpace(parts[1])
	}
	return header
}

func firstNonEmptyNativeMCP(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
