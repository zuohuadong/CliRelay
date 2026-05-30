package api

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var zaiMCPServerURLs = map[string]string{
	"web-search-prime": "https://api.z.ai/api/mcp/web_search_prime/mcp",
	"web_search_prime": "https://api.z.ai/api/mcp/web_search_prime/mcp",
	"search":           "https://api.z.ai/api/mcp/web_search_prime/mcp",
	"web-reader":       "https://api.z.ai/api/mcp/web_reader/mcp",
	"web_reader":       "https://api.z.ai/api/mcp/web_reader/mcp",
	"reader":           "https://api.z.ai/api/mcp/web_reader/mcp",
	"zread":            "https://api.z.ai/api/mcp/zread/mcp",
}

func (s *Server) proxyZAIMCP(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if c.Request.Method == http.MethodOptions {
		c.Status(http.StatusNoContent)
		return
	}
	targetURL, ok := zaiMCPProxyTarget(c.Param("server"), c.Param("path"), c.Request.URL.RawQuery)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unsupported Z.AI MCP server"})
		return
	}
	auth, err := s.selectZAIMCPAuth()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}

	upstreamReq, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, targetURL, c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid MCP proxy request"})
		return
	}
	copyMCPProxyRequestHeaders(upstreamReq.Header, c.Request.Header)
	upstreamReq.Host = ""

	if s.handlers == nil || s.handlers.AuthManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager is not available"})
		return
	}
	if err := s.handlers.AuthManager.PrepareHttpRequest(c.Request.Context(), auth, upstreamReq); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "failed to prepare upstream MCP request"})
		return
	}
	if !executor.ApplyCodexIdentityFingerprintHeaders(s.cfg, upstreamReq.Header, false) {
		executor.ApplyDefaultCodexIdentityFingerprintHeaders(upstreamReq.Header, false)
	}

	resp, err := s.handlers.AuthManager.HttpRequest(c.Request.Context(), auth, upstreamReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream MCP request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			// The response body is already being streamed to the client.
		}
	}()

	copyMCPProxyResponseHeaders(c.Writer.Header(), resp.Header)
	c.Status(resp.StatusCode)
	if _, errCopy := io.Copy(c.Writer, resp.Body); errCopy != nil {
		return
	}
	if flusher, okFlush := c.Writer.(http.Flusher); okFlush {
		flusher.Flush()
	}
}

func zaiMCPProxyTarget(serverName, extraPath, rawQuery string) (string, bool) {
	serverName = strings.ToLower(strings.TrimSpace(serverName))
	baseURL := strings.TrimSpace(zaiMCPServerURLs[serverName])
	if baseURL == "" {
		return "", false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", false
	}
	extraPath = strings.TrimSpace(extraPath)
	if extraPath != "" && extraPath != "/" {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(extraPath, "/")
	}
	if rawQuery != "" {
		parsed.RawQuery = rawQuery
	}
	return parsed.String(), true
}

func (s *Server) selectZAIMCPAuth() (*coreauth.Auth, error) {
	if s == nil || s.handlers == nil || s.handlers.AuthManager == nil {
		return nil, fmt.Errorf("auth manager is not available")
	}
	auths := s.handlers.AuthManager.List()
	candidates := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
			continue
		}
		if !isBigModelCodingAuth(auth) {
			continue
		}
		if auth.Attributes == nil || strings.TrimSpace(auth.Attributes["api_key"]) == "" {
			continue
		}
		candidates = append(candidates, auth)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no active bigmodel-coding auth available for Z.AI MCP proxy")
	}
	index := int(s.mcpProxyCounter.Add(1)-1) % len(candidates)
	selected := candidates[index]
	if s.handlers.AuthManager != nil {
		if current, ok := s.handlers.AuthManager.GetByID(selected.ID); ok && current != nil {
			return current, nil
		}
	}
	return selected, nil
}

func isBigModelCodingAuth(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), config.DefaultBigModelCodingProviderName) {
		return true
	}
	if auth.Attributes == nil {
		return false
	}
	for _, key := range []string{"provider_key", "compat_name"} {
		if strings.EqualFold(strings.TrimSpace(auth.Attributes[key]), config.DefaultBigModelCodingProviderName) {
			return true
		}
	}
	return false
}

func copyMCPProxyRequestHeaders(dst, src http.Header) {
	for key, values := range src {
		if isMCPProxyHopByHopHeader(key) || strings.EqualFold(key, "Authorization") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	if strings.TrimSpace(dst.Get("Accept")) == "" {
		dst.Set("Accept", "application/json, text/event-stream")
	}
}

func copyMCPProxyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if isMCPProxyHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isMCPProxyHopByHopHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "host", "content-length":
		return true
	default:
		return false
	}
}
