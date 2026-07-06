package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// dispatchMCPJSONRPC handles a single MCP JSON-RPC request body, delegating to
// the provided method handler. It manages session ID headers, notification
// suppression, and error envelopes so concrete MCP handlers stay small.
func (s *Server) dispatchMCPJSONRPC(c *gin.Context, methodHandler func(c *gin.Context, req mcpGatewayRequest) (any, *mcpGatewayError)) {
	if c == nil || c.Request == nil {
		return
	}
	if c.Request.Method == http.MethodOptions {
		c.Status(http.StatusNoContent)
		return
	}
	if c.Request.Method != http.MethodPost {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "MCP route requires POST"})
		return
	}

	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 4<<20))
	if err != nil {
		s.writeMCPGatewayError(c, nil, -32700, "failed to read MCP request")
		return
	}
	var req mcpGatewayRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeMCPGatewayError(c, nil, -32700, "invalid JSON-RPC request")
		return
	}
	if len(req.ID) == 0 && req.Method == "notifications/initialized" {
		c.Status(http.StatusAccepted)
		return
	}

	result, rpcErr := methodHandler(c, req)
	if rpcErr != nil {
		s.writeMCPGatewayError(c, req.ID, rpcErr.Code, rpcErr.Message)
		return
	}
	sessionID := strings.TrimSpace(c.GetHeader("Mcp-Session-Id"))
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	c.Header("Mcp-Session-Id", sessionID)
	c.JSON(http.StatusOK, mcpGatewayResponse{
		JSONRPC: "2.0",
		ID:      normalizedMCPGatewayID(req.ID),
		Result:  result,
	})
}
