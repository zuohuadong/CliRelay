package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
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
	if c != nil && c.Request != nil && c.Request.Method == http.MethodGet {
		if requestAcceptsMCPSSE(c.Request) {
			writeMCPMethodNotAllowed(c)
			return
		}
		c.JSON(http.StatusOK, s.mcpGatewayCatalog(c))
		return
	}
	s.dispatchMCPJSONRPC(c, s.handleMCPGatewayMethod)
}

func (s *Server) handleMCPGatewayMethod(c *gin.Context, req mcpGatewayRequest) (any, *mcpGatewayError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "clirelay-mcp-directory", "version": "1.0.0"},
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
	streamableUsage := []string{
		"Configure this route as a Streamable HTTP MCP server.",
		"Call tools/list on this route to discover available tools.",
		"Use Authorization: Bearer <CliRelay API key>.",
	}
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
			Name:        "image",
			Path:        base + "/mcp/image",
			Type:        "builtin",
			Description: "Image generation and editing MCP server. Model-agnostic; defaults to gpt-image-2.",
			Tools:       []string{"clirelay_image_models", "clirelay_image_generate", "clirelay_image_edit"},
			Usage: append([]string{
				"Use the model parameter to select any available image model; default is gpt-image-2.",
				"Use direct /v1/images endpoints for streaming or partial image events.",
			}, streamableUsage...),
			Configured: true,
		},
		{
			Name:        "video",
			Path:        base + "/mcp/video",
			Type:        "builtin",
			Description: "Video generation MCP server. Model-agnostic; defaults to agnes-video-v2.0.",
			Tools:       []string{"clirelay_video_models", "clirelay_video_create", "clirelay_video_status", "clirelay_video_content_url"},
			Usage: append([]string{
				"Use the model parameter to select any available video model; default is agnes-video-v2.0.",
			}, streamableUsage...),
			Configured: true,
		},
	}

	for _, route := range zaiMCPRouteCatalog(base, streamableUsage) {
		routes = append(routes, route)
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
				Usage:       streamableUsage,
				Configured:  true,
			})
		}
	}

	sort.SliceStable(routes, func(i, j int) bool {
		return routes[i].Name < routes[j].Name
	})
	return routes
}

func zaiMCPRouteCatalog(base string, usage []string) []mcpRouteDescriptor {
	aliasesByURL := make(map[string][]string)
	for alias, target := range zaiMCPServerURLs {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		aliasesByURL[target] = append(aliasesByURL[target], alias)
	}

	routes := make([]mcpRouteDescriptor, 0, len(aliasesByURL))
	for target, aliases := range aliasesByURL {
		sort.Strings(aliases)
		tool := zaiMCPToolNameFromURL(target)
		name := preferredZAIMCPRouteName(aliases, tool)
		routes = append(routes, mcpRouteDescriptor{
			Name:        "zai-" + name,
			Path:        base + "/mcp/zai/" + name,
			Type:        "proxy",
			Description: "Z.AI " + name + " MCP server.",
			Tools:       []string{tool},
			Aliases:     aliases,
			Usage:       usage,
			Configured:  true,
		})
	}
	return routes
}

func preferredZAIMCPRouteName(aliases []string, tool string) string {
	for _, alias := range aliases {
		if strings.Contains(alias, "-") {
			return alias
		}
	}
	for _, alias := range aliases {
		if alias == tool {
			return alias
		}
	}
	if len(aliases) > 0 {
		return aliases[0]
	}
	return tool
}

func zaiMCPToolNameFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part != "" && part != "mcp" {
			return part
		}
	}
	return ""
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
