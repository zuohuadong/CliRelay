package api

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type mcpGatewayRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpGatewayResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *mcpGatewayError `json:"error,omitempty"`
}

type mcpGatewayError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpGatewayToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type mcpRouteDescriptor struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Tools       []string `json:"tools,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Usage       []string `json:"usage,omitempty"`
	Configured  bool     `json:"configured,omitempty"`
}

func (s *Server) handleMCPGateway(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if c.Request.Method == http.MethodOptions {
		c.Status(http.StatusNoContent)
		return
	}
	if c.Request.Method == http.MethodGet {
		c.JSON(http.StatusOK, s.mcpGatewayCatalog(c))
		return
	}
	if c.Request.Method != http.MethodPost {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "MCP gateway requires POST"})
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

	result, rpcErr := s.handleMCPGatewayMethod(c, req)
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

func (s *Server) handleMCPGatewayMethod(c *gin.Context, req mcpGatewayRequest) (any, *mcpGatewayError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "clirelay-mcp-directory",
				"version": "1.0.0",
			},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.mcpGatewayTools()}, nil
	case "tools/call":
		var params mcpGatewayToolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &mcpGatewayError{Code: -32602, Message: "invalid tool call params"}
		}
		return s.callMCPGatewayTool(c, params)
	default:
		return nil, &mcpGatewayError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) callMCPGatewayTool(c *gin.Context, params mcpGatewayToolCallParams) (any, *mcpGatewayError) {
	switch strings.TrimSpace(params.Name) {
	case "clirelay_mcp_routes":
		return mcpGatewayToolJSON(s.mcpGatewayCatalog(c)), nil
	case "clirelay_mcp_route_info":
		name := strings.TrimSpace(stringMCPGatewayArg(params.Arguments, "name"))
		if name == "" {
			return nil, &mcpGatewayError{Code: -32602, Message: "name is required"}
		}
		for _, route := range s.mcpGatewayRoutes(c) {
			if route.Name == name || route.Path == name {
				return mcpGatewayToolJSON(route), nil
			}
		}
		return nil, &mcpGatewayError{Code: -32602, Message: "unknown MCP route: " + name}
	default:
		return nil, &mcpGatewayError{Code: -32602, Message: "unknown tool: " + params.Name}
	}
}

func (s *Server) mcpGatewayCatalog(c *gin.Context) map[string]any {
	return map[string]any{
		"server": map[string]any{
			"name":        "clirelay-mcp-directory",
			"description": "Directory of CliRelay MCP routes. Concrete MCP tools are served by child routes.",
		},
		"routes":          s.mcpGatewayRoutes(c),
		"discovery_tools": s.mcpGatewayTools(),
	}
}

func (s *Server) mcpGatewayRoutes(c *gin.Context) []mcpRouteDescriptor {
	base := strings.TrimRight(s.mcpGatewayExternalBaseURL(c), "/")
	routes := []mcpRouteDescriptor{
		{
			Name:        "directory",
			Path:        base + "/mcp",
			Type:        "directory",
			Description: "Lists available CliRelay MCP routes and discovery tools.",
			Tools:       []string{"clirelay_mcp_routes", "clirelay_mcp_route_info"},
			Usage:       []string{"Use this route for discovery only; configure a child route URL to call concrete MCP tools."},
		},
		{
			Name:        "video",
			Path:        base + "/mcp/video",
			Type:        "builtin",
			Description: "Video generation MCP server.",
			Tools:       []string{"clirelay_video_models", "clirelay_video_create", "clirelay_video_status", "clirelay_video_content_url"},
			Usage:       []string{"Configure this route as a Streamable HTTP MCP server.", "Call tools/list on this route to discover video tools.", "Use clirelay_video_create with the model parameter to select any available video model; default is agnes-video-v2.0.", "Use Authorization: Bearer <CliRelay API key>."},
			Configured:  true,
		},
		{
			Name:        "zai-web-search-prime",
			Path:        base + "/mcp/zai/web-search-prime",
			Type:        "proxy",
			Description: "Z.AI web_search_prime MCP server.",
			Tools:       []string{"web_search_prime"},
			Aliases:     []string{"web_search_prime", "search"},
			Usage:       []string{"Configure this route as a Streamable HTTP MCP server.", "Call tools/list on this route to discover upstream tools.", "Use Authorization: Bearer <CliRelay API key>."},
			Configured:  true,
		},
		{
			Name:        "zai-web-reader",
			Path:        base + "/mcp/zai/web-reader",
			Type:        "proxy",
			Description: "Z.AI web_reader MCP server.",
			Tools:       []string{"web_reader"},
			Aliases:     []string{"web_reader", "reader"},
			Usage:       []string{"Configure this route as a Streamable HTTP MCP server.", "Call tools/list on this route to discover upstream tools.", "Use Authorization: Bearer <CliRelay API key>."},
			Configured:  true,
		},
		{
			Name:        "zai-zread",
			Path:        base + "/mcp/zai/zread",
			Type:        "proxy",
			Description: "Z.AI zread MCP server.",
			Tools:       []string{"zread"},
			Usage:       []string{"Configure this route as a Streamable HTTP MCP server.", "Call tools/list on this route to discover upstream tools.", "Use Authorization: Bearer <CliRelay API key>."},
			Configured:  true,
		},
	}

	if s != nil && s.cfg != nil {
		for _, upstream := range s.cfg.MCPProxy.Servers {
			if upstream.Disabled {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(upstream.Name))
			if name == "" {
				continue
			}
			routes = append(routes, mcpRouteDescriptor{
				Name:        "custom-" + name,
				Path:        base + "/mcp/custom/" + name,
				Type:        "proxy",
				Description: "Configured custom MCP proxy server.",
				Usage:       []string{"Configure this route as a Streamable HTTP MCP server.", "Call tools/list on this route to discover upstream tools.", "Use Authorization: Bearer <CliRelay API key>."},
				Configured:  true,
			})
		}
	}

	sort.SliceStable(routes, func(i, j int) bool {
		return routes[i].Name < routes[j].Name
	})
	return routes
}

func (s *Server) mcpGatewayTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "clirelay_mcp_routes",
			"description": "List available CliRelay MCP routes. Concrete tools are served by those child route URLs.",
			"inputSchema": mcpGatewayObjectSchema(nil, nil),
		},
		{
			"name":        "clirelay_mcp_route_info",
			"description": "Get details for one MCP route by name or URL.",
			"inputSchema": mcpGatewayObjectSchema(map[string]any{
				"name": mcpGatewayStringSchema("Route name or full route URL."),
			}, []string{"name"}),
		},
	}
}

func (s *Server) mcpGatewayExternalBaseURL(c *gin.Context) string {
	proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto"))
	if proto == "" {
		proto = "http"
		if c.Request != nil && c.Request.TLS != nil {
			proto = "https"
		}
	}
	host := ""
	if c.Request != nil {
		host = strings.TrimSpace(c.Request.Host)
	}
	if forwarded := strings.TrimSpace(c.GetHeader("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	return strings.TrimRight(proto+"://"+host, "/")
}

func (s *Server) writeMCPGatewayError(c *gin.Context, id json.RawMessage, code int, message string) {
	c.JSON(http.StatusOK, mcpGatewayResponse{
		JSONRPC: "2.0",
		ID:      normalizedMCPGatewayID(id),
		Error:   &mcpGatewayError{Code: code, Message: message},
	})
}

func normalizedMCPGatewayID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func mcpGatewayToolJSON(value any) map[string]any {
	raw, _ := json.MarshalIndent(value, "", "  ")
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(raw)},
		},
	}
}

func stringMCPGatewayArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch typed := args[key].(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		raw, _ := json.Marshal(typed)
		return string(raw)
	}
}

func mcpGatewayObjectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func mcpGatewayStringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func mcpGatewayNumberSchema(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func mcpGatewayBoolSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}
