package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

const defaultMCPImageModel = "gpt-image-2"

func (s *Server) handleImageMCP(c *gin.Context) {
	s.dispatchMCPJSONRPC(c, s.handleImageMCPMethod)
}

func (s *Server) handleImageMCPMethod(c *gin.Context, req mcpGatewayRequest) (any, *mcpGatewayError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "clirelay-image", "version": "1.0.0"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": imageMCPTools()}, nil
	case "tools/call":
		var params mcpGatewayToolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &mcpGatewayError{Code: -32602, Message: "invalid tool call params"}
		}
		return s.callImageMCPTool(c, params)
	default:
		return nil, &mcpGatewayError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) callImageMCPTool(c *gin.Context, params mcpGatewayToolCallParams) (any, *mcpGatewayError) {
	args := params.Arguments
	if args == nil {
		args = map[string]any{}
	}
	switch strings.TrimSpace(params.Name) {
	case "clirelay_image_models":
		return mcpGatewayToolJSON(s.imageMCPModelList(c)), nil
	case "clirelay_image_generate":
		payload, rpcErr := buildImageGeneratePayload(args)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if rpcErr := validateMCPModelAccess(c, stringMCPValue(payload["model"])); rpcErr != nil {
			return nil, rpcErr
		}
		out, rpcErr := s.imageMCPGenerate(c, payload)
		if rpcErr != nil {
			return nil, rpcErr
		}
		return mcpGatewayToolJSON(out), nil
	case "clirelay_image_edit":
		payload, rpcErr := buildImageEditPayload(args)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if rpcErr := validateMCPModelAccess(c, stringMCPValue(payload["model"])); rpcErr != nil {
			return nil, rpcErr
		}
		out, rpcErr := s.imageMCPEdit(c, payload)
		if rpcErr != nil {
			return nil, rpcErr
		}
		return mcpGatewayToolJSON(out), nil
	default:
		return nil, &mcpGatewayError{Code: -32602, Message: "unknown tool: " + params.Name}
	}
}

func imageMCPTools() []map[string]any {
	common := map[string]any{
		"prompt":             mcpGatewayStringSchema("Image prompt."),
		"model":              mcpGatewayStringSchema("Image model. Defaults to gpt-image-2."),
		"size":               mcpGatewayStringSchema("Image size such as 1024x1024."),
		"quality":            mcpGatewayStringSchema("Image quality such as high, medium, or low."),
		"background":         mcpGatewayStringSchema("Background option such as opaque or transparent."),
		"output_format":      mcpGatewayStringSchema("Output format such as png, jpeg, or webp."),
		"response_format":    mcpGatewayStringSchema("Response format: b64_json or url."),
		"n":                  mcpGatewayNumberSchema("Number of images to request."),
		"output_compression": mcpGatewayNumberSchema("Output compression level for supported formats."),
	}
	editProps := cloneMCPProperties(common)
	editProps["image_url"] = mcpGatewayStringSchema("Single source image URL or data URL.")
	editProps["images"] = map[string]any{
		"type":        "array",
		"description": "Source image URLs, data URLs, or image objects accepted by /v1/images/edits.",
		"items":       map[string]any{},
	}
	editProps["mask_image_url"] = mcpGatewayStringSchema("Optional mask image URL or data URL.")

	return []map[string]any{
		{
			"name":        "clirelay_image_models",
			"description": "List image-capable models available through CliRelay.",
			"inputSchema": mcpGatewayObjectSchema(nil, nil),
		},
		{
			"name":        "clirelay_image_generate",
			"description": "Generate images through the OpenAI-compatible Images API.",
			"inputSchema": mcpGatewayObjectSchema(common, []string{"prompt"}),
		},
		{
			"name":        "clirelay_image_edit",
			"description": "Edit an image through the OpenAI-compatible Images API. Provide image_url or images.",
			"inputSchema": mcpGatewayObjectSchema(editProps, []string{"prompt"}),
		},
	}
}

func cloneMCPProperties(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func buildImageGeneratePayload(args map[string]any) (map[string]any, *mcpGatewayError) {
	prompt := strings.TrimSpace(stringMCPGatewayArg(args, "prompt"))
	if prompt == "" {
		return nil, &mcpGatewayError{Code: -32602, Message: "prompt is required"}
	}
	payload := baseImagePayload(args, prompt)
	return payload, nil
}

func buildImageEditPayload(args map[string]any) (map[string]any, *mcpGatewayError) {
	prompt := strings.TrimSpace(stringMCPGatewayArg(args, "prompt"))
	if prompt == "" {
		return nil, &mcpGatewayError{Code: -32602, Message: "prompt is required"}
	}
	payload := baseImagePayload(args, prompt)
	if imageURL := strings.TrimSpace(stringMCPGatewayArg(args, "image_url")); imageURL != "" {
		payload["images"] = []map[string]any{{"image_url": imageURL}}
	} else if images, ok := normalizeMCPImageInputs(args["images"]); ok {
		payload["images"] = images
	} else {
		return nil, &mcpGatewayError{Code: -32602, Message: "image_url or images is required"}
	}
	if maskURL := strings.TrimSpace(stringMCPGatewayArg(args, "mask_image_url")); maskURL != "" {
		payload["mask"] = map[string]any{"image_url": maskURL}
	}
	return payload, nil
}

func baseImagePayload(args map[string]any, prompt string) map[string]any {
	model := strings.TrimSpace(stringMCPGatewayArg(args, "model"))
	if model == "" {
		model = defaultMCPImageModel
	}
	payload := map[string]any{
		"model":  model,
		"prompt": prompt,
	}
	for _, key := range []string{"size", "quality", "background", "output_format", "response_format", "moderation", "aspect_ratio", "resolution"} {
		if value := strings.TrimSpace(stringMCPGatewayArg(args, key)); value != "" {
			payload[key] = value
		}
	}
	for _, key := range []string{"n", "output_compression", "partial_images"} {
		if value := intMCPArg(args, key); value > 0 {
			payload[key] = value
		}
	}
	return payload
}

func normalizeMCPImageInputs(value any) ([]map[string]any, bool) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				out = append(out, map[string]any{"image_url": trimmed})
			}
		case map[string]any:
			if imageURL := strings.TrimSpace(stringMCPValue(typed["image_url"])); imageURL != "" {
				out = append(out, map[string]any{"image_url": imageURL})
				continue
			}
			if fileID := strings.TrimSpace(stringMCPValue(typed["file_id"])); fileID != "" {
				out = append(out, map[string]any{"file_id": fileID})
			}
		}
	}
	return out, len(out) > 0
}

func (s *Server) imageMCPModelList(c *gin.Context) map[string]any {
	reg := registry.GetGlobalRegistry()
	all := reg.GetAvailableModels("openai")
	data := make([]map[string]any, 0, len(all)+2)
	allowed := mcpAllowedModels(c)
	seen := make(map[string]struct{}, len(all)+4)
	for _, model := range []struct {
		id      string
		ownedBy string
	}{
		{id: "gpt-image-1.5", ownedBy: "openai"},
		{id: defaultMCPImageModel, ownedBy: "openai"},
		{id: "grok-imagine-image", ownedBy: "xai"},
		{id: "grok-imagine-image-quality", ownedBy: "xai"},
	} {
		if !mcpModelAllowed(model.id, allowed) {
			continue
		}
		seen[strings.ToLower(model.id)] = struct{}{}
		data = append(data, map[string]any{"id": model.id, "object": "model", "owned_by": model.ownedBy, "type": registry.OpenAIImageModelType})
	}
	for _, model := range all {
		modelType, _ := model["type"].(string)
		if modelType != registry.OpenAIImageModelType {
			continue
		}
		modelID := stringMCPValue(model["id"])
		if !mcpModelAllowed(modelID, allowed) {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(modelID))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		data = append(data, model)
	}
	return map[string]any{"object": "list", "data": data}
}

func (s *Server) imageMCPGenerate(c *gin.Context, payload map[string]any) (map[string]any, *mcpGatewayError) {
	return s.callImageHandler(c, payload, "/openai/v1/images/generations", "generate")
}

func (s *Server) imageMCPEdit(c *gin.Context, payload map[string]any) (map[string]any, *mcpGatewayError) {
	return s.callImageHandler(c, payload, "/openai/v1/images/edits", "edit")
}

func (s *Server) callImageHandler(c *gin.Context, payload map[string]any, path string, op string) (map[string]any, *mcpGatewayError) {
	if s.openaiHandler == nil {
		return nil, &mcpGatewayError{Code: -32603, Message: "image handler not available"}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, &mcpGatewayError{Code: -32603, Message: "failed to encode payload"}
	}

	rec := httptest.NewRecorder()
	innerCtx, _ := gin.CreateTestContext(rec)
	innerCtx.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	if c != nil && c.Request != nil {
		innerCtx.Request = innerCtx.Request.WithContext(c.Request.Context())
	}
	innerCtx.Request.Header.Set("Content-Type", "application/json")
	copyMCPContextValues(c, innerCtx)

	if strings.HasSuffix(path, "/generations") {
		s.openaiHandler.ImagesGenerations(innerCtx)
	} else {
		s.openaiHandler.ImagesEdits(innerCtx)
	}

	return parseMCPImageResponse(rec, op)
}

func parseMCPImageResponse(rec *httptest.ResponseRecorder, op string) (map[string]any, *mcpGatewayError) {
	body := rec.Body.Bytes()
	if rec.Code < 200 || rec.Code >= 300 {
		errMsg := gjson.GetBytes(body, "error.message").String()
		if errMsg == "" {
			errMsg = fmt.Sprintf("image %s failed: HTTP %d", op, rec.Code)
		}
		return nil, &mcpGatewayError{Code: -32000, Message: errMsg}
	}
	var out map[string]any
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, &mcpGatewayError{Code: -32603, Message: fmt.Sprintf("decode image %s response: %v", op, err)}
	}
	return out, nil
}

func validateMCPModelAccess(c *gin.Context, model string) *mcpGatewayError {
	if !mcpModelAllowed(model, mcpAllowedModels(c)) {
		return &mcpGatewayError{Code: -32000, Message: "model not allowed for this API key"}
	}
	return nil
}
