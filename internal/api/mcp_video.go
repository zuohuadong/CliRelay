package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

const defaultMCPVideoModel = "agnes-video-v2.0"

func (s *Server) handleVideoMCP(c *gin.Context) {
	s.dispatchMCPJSONRPC(c, s.handleVideoMCPMethod)
}

func (s *Server) handleVideoMCPMethod(c *gin.Context, req mcpGatewayRequest) (any, *mcpGatewayError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "clirelay-video", "version": "1.0.0"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": videoMCPTools()}, nil
	case "tools/call":
		var params mcpGatewayToolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &mcpGatewayError{Code: -32602, Message: "invalid tool call params"}
		}
		return s.callVideoMCPTool(c, params)
	default:
		return nil, &mcpGatewayError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) callVideoMCPTool(c *gin.Context, params mcpGatewayToolCallParams) (any, *mcpGatewayError) {
	args := params.Arguments
	if args == nil {
		args = map[string]any{}
	}
	switch strings.TrimSpace(params.Name) {
	case "clirelay_video_models":
		return mcpGatewayToolJSON(s.videoMCPModelList(c)), nil
	case "clirelay_video_create":
		payload, rpcErr := buildVideoCreatePayload(args)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if rpcErr := validateMCPVideoModelAccess(c, stringMCPValue(payload["model"])); rpcErr != nil {
			return nil, rpcErr
		}
		out, rpcErr := s.videoMCPCreate(c, payload)
		if rpcErr != nil {
			return nil, rpcErr
		}
		return mcpGatewayToolJSON(out), nil
	case "clirelay_video_status":
		videoID := strings.TrimSpace(stringMCPGatewayArg(args, "video_id"))
		if videoID == "" {
			return nil, &mcpGatewayError{Code: -32602, Message: "video_id is required"}
		}
		out, rpcErr := s.videoMCPStatus(c, videoID)
		if rpcErr != nil {
			return nil, rpcErr
		}
		return mcpGatewayToolJSON(out), nil
	case "clirelay_video_content_url":
		videoID := strings.TrimSpace(stringMCPGatewayArg(args, "video_id"))
		if videoID == "" {
			return nil, &mcpGatewayError{Code: -32602, Message: "video_id is required"}
		}
		return mcpGatewayToolJSON(map[string]any{
			"video_id":    videoID,
			"content_url": s.mcpGatewayExternalBaseURL(c) + "/openai/v1/videos/" + url.PathEscape(videoID) + "/content",
		}), nil
	default:
		return nil, &mcpGatewayError{Code: -32602, Message: "unknown tool: " + params.Name}
	}
}

func videoMCPTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "clirelay_video_models",
			"description": "List video-capable models available through CliRelay.",
			"inputSchema": mcpGatewayObjectSchema(nil, nil),
		},
		{
			"name":        "clirelay_video_create",
			"description": "Create a video generation task. Returns a video_id immediately; poll with clirelay_video_status.",
			"inputSchema": mcpGatewayObjectSchema(map[string]any{
				"prompt":       mcpGatewayStringSchema("Video prompt."),
				"model":        mcpGatewayStringSchema("Video model. Defaults to agnes-video-v2.0."),
				"seconds":      mcpGatewayNumberSchema("Video duration in seconds."),
				"size":         mcpGatewayStringSchema("Video size such as 720x1280."),
				"aspect_ratio": mcpGatewayStringSchema("Aspect ratio such as 9:16 or 16:9."),
				"resolution":   mcpGatewayStringSchema("Resolution such as 720p."),
			}, []string{"prompt"}),
		},
		{
			"name":        "clirelay_video_status",
			"description": "Get the current status and video_url for a video task.",
			"inputSchema": mcpGatewayObjectSchema(map[string]any{
				"video_id": mcpGatewayStringSchema("Video id returned by create."),
			}, []string{"video_id"}),
		},
		{
			"name":        "clirelay_video_content_url",
			"description": "Return the authenticated HTTP content endpoint for a completed video.",
			"inputSchema": mcpGatewayObjectSchema(map[string]any{
				"video_id": mcpGatewayStringSchema("Video id returned by create."),
			}, []string{"video_id"}),
		},
	}
}

func buildVideoCreatePayload(args map[string]any) (map[string]any, *mcpGatewayError) {
	prompt := strings.TrimSpace(stringMCPGatewayArg(args, "prompt"))
	if prompt == "" {
		return nil, &mcpGatewayError{Code: -32602, Message: "prompt is required"}
	}
	model := strings.TrimSpace(stringMCPGatewayArg(args, "model"))
	if model == "" {
		model = defaultMCPVideoModel
	}
	payload := map[string]any{
		"model":  model,
		"prompt": prompt,
	}
	if seconds := intMCPArg(args, "seconds"); seconds > 0 {
		payload["seconds"] = seconds
	}
	for _, key := range []string{"size", "aspect_ratio", "resolution"} {
		if value := strings.TrimSpace(stringMCPGatewayArg(args, key)); value != "" {
			payload[key] = value
		}
	}
	return payload, nil
}

// videoMCPModelList returns video models from the global registry using the
// proper type marker instead of string-matching the model name.
func (s *Server) videoMCPModelList(c *gin.Context) map[string]any {
	reg := registry.GetGlobalRegistry()
	all := reg.GetAvailableModels("openai")
	data := make([]map[string]any, 0, len(all))
	allowed := mcpAllowedModels(c)
	for _, model := range all {
		modelType, _ := model["type"].(string)
		if modelType != registry.OpenAIVideoModelType {
			continue
		}
		if !mcpModelAllowed(stringMCPValue(model["id"]), allowed) {
			continue
		}
		data = append(data, model)
	}
	return map[string]any{"object": "list", "data": data}
}

// videoMCPCreate dispatches directly to the OpenAI video handler, bypassing
// the middleware stack to avoid double auth/billing.
func (s *Server) videoMCPCreate(c *gin.Context, payload map[string]any) (map[string]any, *mcpGatewayError) {
	if s.openaiHandler == nil {
		return nil, &mcpGatewayError{Code: -32603, Message: "video handler not available"}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, &mcpGatewayError{Code: -32603, Message: "failed to encode payload"}
	}

	rec := httptest.NewRecorder()
	innerCtx, _ := gin.CreateTestContext(rec)
	innerCtx.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/videos", strings.NewReader(string(raw)))
	if c != nil && c.Request != nil {
		innerCtx.Request = innerCtx.Request.WithContext(c.Request.Context())
	}
	innerCtx.Request.Header.Set("Content-Type", "application/json")
	copyMCPContextValues(c, innerCtx)

	s.openaiHandler.VideosCreate(innerCtx)

	return parseMCPVideoResponse(rec, "create")
}

// videoMCPStatus dispatches directly to the OpenAI video retrieval handler.
func (s *Server) videoMCPStatus(c *gin.Context, videoID string) (map[string]any, *mcpGatewayError) {
	if s.openaiHandler == nil {
		return nil, &mcpGatewayError{Code: -32603, Message: "video handler not available"}
	}

	rec := httptest.NewRecorder()
	innerCtx, _ := gin.CreateTestContext(rec)
	innerCtx.Request = httptest.NewRequest(http.MethodGet, "/openai/v1/videos/"+url.PathEscape(videoID), nil)
	if c != nil && c.Request != nil {
		innerCtx.Request = innerCtx.Request.WithContext(c.Request.Context())
	}
	innerCtx.Params = gin.Params{{Key: "video_id", Value: videoID}}
	copyMCPContextValues(c, innerCtx)

	s.openaiHandler.VideosRetrieve(innerCtx)

	return parseMCPVideoResponse(rec, "status")
}

// copyMCPContextValues propagates auth context from the MCP caller into the
// inner gin context used for direct handler dispatch.
func copyMCPContextValues(src, dst *gin.Context) {
	if src == nil || dst == nil {
		return
	}
	if authHeader := strings.TrimSpace(src.GetHeader("Authorization")); authHeader != "" {
		dst.Request.Header.Set("Authorization", authHeader)
	}
	for _, key := range []string{"userApiKey", "accessProvider", "accessMetadata"} {
		if val, exists := src.Get(key); exists {
			dst.Set(key, val)
		}
	}
}

func parseMCPVideoResponse(rec *httptest.ResponseRecorder, op string) (map[string]any, *mcpGatewayError) {
	body := rec.Body.Bytes()
	if rec.Code < 200 || rec.Code >= 300 {
		errMsg := gjson.GetBytes(body, "error.message").String()
		if errMsg == "" {
			errMsg = fmt.Sprintf("video %s failed: HTTP %d", op, rec.Code)
		}
		return nil, &mcpGatewayError{Code: -32000, Message: errMsg}
	}
	var out map[string]any
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, &mcpGatewayError{Code: -32603, Message: fmt.Sprintf("decode video %s response: %v", op, err)}
	}
	return out, nil
}

func intMCPArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		var out int
		_, _ = fmt.Sscanf(strings.TrimSpace(value), "%d", &out)
		return out
	default:
		return 0
	}
}

func validateMCPVideoModelAccess(c *gin.Context, model string) *mcpGatewayError {
	if !mcpModelAllowed(model, mcpAllowedModels(c)) {
		return &mcpGatewayError{Code: -32000, Message: "model not allowed for this API key"}
	}
	return nil
}

func mcpAllowedModels(c *gin.Context) []string {
	if c == nil {
		return nil
	}
	raw, exists := c.Get("accessMetadata")
	if !exists {
		return nil
	}
	metadata, ok := raw.(map[string]string)
	if !ok {
		return nil
	}
	allowedRaw := strings.TrimSpace(metadata["allowed-models"])
	if allowedRaw == "" {
		return nil
	}
	parts := strings.Split(allowedRaw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func mcpModelAllowed(model string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	for _, item := range allowed {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == model {
			return true
		}
		if strings.HasSuffix(item, "*") && strings.HasPrefix(model, strings.TrimSuffix(item, "*")) {
			return true
		}
	}
	return false
}

func stringMCPValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case float64:
		return fmt.Sprintf("%v", typed)
	case nil:
		return ""
	default:
		raw, _ := json.Marshal(typed)
		return string(raw)
	}
}
