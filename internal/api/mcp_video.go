package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const defaultMCPVideoModel = "agnes-video-v2.0"

func (s *Server) handleVideoMCP(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if c.Request.Method == http.MethodOptions {
		c.Status(http.StatusNoContent)
		return
	}
	if c.Request.Method != http.MethodPost {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "Video MCP requires POST"})
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

	result, rpcErr := s.handleVideoMCPMethod(c, req)
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

func (s *Server) handleVideoMCPMethod(c *gin.Context, req mcpGatewayRequest) (any, *mcpGatewayError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "clirelay-mcp-video",
				"version": "1.0.0",
			},
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
		out, err := s.mcpInternalJSON(c, http.MethodGet, "/v1/models", nil)
		if err != nil {
			return nil, &mcpGatewayError{Code: -32000, Message: err.Error()}
		}
		return mcpGatewayToolJSON(filterVideoMCPModels(out)), nil
	case "clirelay_video_create":
		payload, rpcErr := buildVideoCreatePayload(args)
		if rpcErr != nil {
			return nil, rpcErr
		}
		out, err := s.mcpInternalJSON(c, http.MethodPost, "/openai/v1/videos", payload)
		if err != nil {
			return nil, &mcpGatewayError{Code: -32000, Message: err.Error()}
		}
		if boolMCPArg(args, "wait") {
			out, err = s.waitVideoMCP(c, mcpVideoID(out), args)
			if err != nil {
				return mcpGatewayToolJSON(out), nil
			}
		}
		return mcpGatewayToolJSON(out), nil
	case "clirelay_video_status":
		videoID := strings.TrimSpace(stringMCPGatewayArg(args, "video_id"))
		if videoID == "" {
			return nil, &mcpGatewayError{Code: -32602, Message: "video_id is required"}
		}
		out, err := s.mcpInternalJSON(c, http.MethodGet, "/openai/v1/videos/"+url.PathEscape(videoID), nil)
		if err != nil {
			return nil, &mcpGatewayError{Code: -32000, Message: err.Error()}
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
			"description": "Create a video generation task through CliRelay.",
			"inputSchema": mcpGatewayObjectSchema(map[string]any{
				"prompt":                mcpGatewayStringSchema("Video prompt."),
				"model":                 mcpGatewayStringSchema("Video model. Defaults to agnes-video-v2.0."),
				"seconds":               mcpGatewayNumberSchema("Video duration in seconds."),
				"size":                  mcpGatewayStringSchema("Video size such as 720x1280."),
				"aspect_ratio":          mcpGatewayStringSchema("Aspect ratio such as 9:16 or 16:9."),
				"resolution":            mcpGatewayStringSchema("Resolution such as 720p."),
				"wait":                  mcpGatewayBoolSchema("Poll until the video reaches a terminal state."),
				"poll_interval_seconds": mcpGatewayNumberSchema("Polling interval when wait is true."),
				"timeout_seconds":       mcpGatewayNumberSchema("Maximum wait time when wait is true."),
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

func (s *Server) waitVideoMCP(c *gin.Context, videoID string, args map[string]any) (map[string]any, error) {
	if strings.TrimSpace(videoID) == "" {
		return nil, fmt.Errorf("create response did not include a video id")
	}
	timeout := time.Duration(intMCPArg(args, "timeout_seconds")) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	interval := time.Duration(intMCPArg(args, "poll_interval_seconds")) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		out, err := s.mcpInternalJSON(c, http.MethodGet, "/openai/v1/videos/"+url.PathEscape(videoID), nil)
		if err != nil {
			return out, err
		}
		switch strings.ToLower(stringMCPValue(out["status"])) {
		case "completed", "succeeded", "success", "done":
			return out, nil
		case "failed", "error", "cancelled", "canceled", "expired":
			return out, fmt.Errorf("video generation failed: %s", stringMCPValue(out["error"]))
		}
		if time.Now().Add(interval).After(deadline) {
			return out, fmt.Errorf("video generation timed out")
		}
		select {
		case <-c.Request.Context().Done():
			return out, c.Request.Context().Err()
		case <-time.After(interval):
		}
	}
}

func (s *Server) mcpInternalJSON(c *gin.Context, method, path string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authHeader := strings.TrimSpace(c.GetHeader("Authorization")); authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	} else if apiKey, ok := c.Get("userApiKey"); ok {
		if key := strings.TrimSpace(fmt.Sprint(apiKey)); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}

	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)
	raw := rec.Body.Bytes()
	if rec.Code < 200 || rec.Code >= 300 {
		return nil, fmt.Errorf("%s %s failed: %d: %s", method, path, rec.Code, strings.TrimSpace(string(raw)))
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func filterVideoMCPModels(payload map[string]any) map[string]any {
	items, _ := payload["data"].([]any)
	data := make([]any, 0, len(items))
	for _, item := range items {
		model, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(stringMCPValue(model["id"])))
		if id == "" {
			continue
		}
		if strings.Contains(id, "video") || id == strings.ToLower(defaultMCPVideoModel) {
			data = append(data, model)
		}
	}
	return map[string]any{"object": "list", "data": data}
}

func mcpVideoID(payload map[string]any) string {
	for _, key := range []string{"id", "request_id", "video_id"} {
		if value := strings.TrimSpace(stringMCPValue(payload[key])); value != "" {
			return value
		}
	}
	return ""
}

func boolMCPArg(args map[string]any, key string) bool {
	switch value := args[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
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
