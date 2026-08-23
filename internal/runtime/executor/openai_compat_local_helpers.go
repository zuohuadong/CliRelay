package executor

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const (
	openAICompatVideoHandlerType      = "openai-video"
	openAICompatVideosPath            = "/videos"
	openAICompatVideoGenerationsPath  = "/video/generations"
	openAICompatVideosGenerationsPath = "/videos/generations"
	openAICompatDefaultVideoEndpoint  = openAICompatVideosPath
)

func shouldNormalizeKimiCompatPayload(model string) bool {
	model = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	return strings.HasPrefix(model, "kimi-") ||
		strings.Contains(model, "/kimi-") ||
		strings.Contains(model, "moonshot")
}

func openAICompatVideoEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != openAICompatVideoHandlerType {
		return ""
	}
	path := normalizedOpenAICompatEndpointPath(helps.PayloadRequestPath(opts))
	if strings.HasSuffix(path, "/video/generations") {
		return openAICompatVideoGenerationsPath
	}
	if strings.HasSuffix(path, "/videos/generations") {
		return openAICompatVideosGenerationsPath
	}
	if strings.Contains(path, "/videos/") {
		endpoint := strings.TrimPrefix(path, "/v1")
		endpoint = strings.TrimSuffix(endpoint, "/content")
		return endpoint
	}
	return openAICompatDefaultVideoEndpoint
}

func openAICompatPassthroughMethod(opts cliproxyexecutor.Options, endpointPath string) string {
	if opts.SourceFormat.String() == openAICompatVideoHandlerType && (strings.Contains(endpointPath, "/videos/") || strings.Contains(endpointPath, "/agnesapi")) {
		return http.MethodGet
	}
	return http.MethodPost
}

func openAICompatVideoProviderEndpointPath(opts cliproxyexecutor.Options, endpointPath string, payload []byte, model string, auth *cliproxyauth.Auth) string {
	if opts.SourceFormat.String() != openAICompatVideoHandlerType {
		return endpointPath
	}
	if !strings.Contains(endpointPath, "/videos/") {
		return endpointPath
	}
	if strings.HasSuffix(endpointPath, "/videos/generations") {
		return endpointPath
	}
	videoID := strings.TrimSpace(gjson.GetBytes(payload, "video_id").String())
	if isAgnesOpenAICompatVideo(model, auth) {
		if videoID == "" {
			return endpointPath
		}
		return "/agnesapi?video_id=" + url.QueryEscape(videoID)
	}
	if videoID == "" {
		videoID = strings.TrimSpace(gjson.GetBytes(payload, "request_id").String())
	}
	if videoID == "" {
		return endpointPath
	}
	return "/videos/" + url.PathEscape(videoID)
}

func isAgnesOpenAICompatVideo(model string, auth *cliproxyauth.Auth) bool {
	model = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if strings.Contains(model, "agnes-video") {
		return true
	}
	if auth == nil {
		return false
	}
	label := strings.ToLower(strings.TrimSpace(auth.Label))
	if label == "agnes" || strings.Contains(label, "agnes") {
		return true
	}
	for _, key := range []string{"compat_name", "provider_key"} {
		value := strings.ToLower(strings.TrimSpace(auth.Attributes[key]))
		if value == "agnes" || value == "agnes-ai" {
			return true
		}
	}
	return false
}

func normalizedOpenAICompatEndpointPath(path string) string {
	path = strings.TrimSpace(path)
	for _, prefix := range []string{"/openai/v1", "/v1"} {
		if strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
			if path == "" {
				return "/"
			}
			return path
		}
	}
	return path
}

func (e *OpenAICompatExecutor) useResponsesEndpoint(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) bool {
	if opts.Alt != "" {
		return false
	}
	sourceFormat := opts.SourceFormat.String()
	if sourceFormat != "openai" && sourceFormat != "openai-response" {
		return false
	}
	if auth != nil && auth.Attributes != nil {
		if strings.EqualFold(strings.TrimSpace(auth.Attributes["response_endpoint"]), "true") {
			return true
		}
		if sourceFormat == "openai-response" && strings.EqualFold(strings.TrimSpace(auth.Attributes["identity_fingerprint"]), "codex") {
			return true
		}
	}
	compat := e.resolveCompatConfig(auth)
	if compat == nil {
		return false
	}
	if compat.ResponseEndpoint {
		return true
	}
	return sourceFormat == "openai-response" && strings.EqualFold(strings.TrimSpace(compat.IdentityFingerprint), "codex")
}

func (e *OpenAICompatExecutor) useResponsesCompactEndpoint(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) bool {
	if opts.Alt != "responses/compact" {
		return false
	}
	probeOpts := opts
	probeOpts.Alt = ""
	return e.useResponsesEndpoint(auth, probeOpts)
}

func (e *OpenAICompatExecutor) normalizeBigModelTools(payload []byte, baseURL string) ([]byte, error) {
	if !isBigModelCompatProvider(e.Identifier(), baseURL) || !gjson.GetBytes(payload, "tools").Exists() {
		return payload, nil
	}
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, fmt.Errorf("normalize bigmodel tools: invalid payload: %w", err)
	}
	tools, ok := root["tools"].([]any)
	if !ok {
		return payload, nil
	}
	changed := false
	for i, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		toolType := strings.ToLower(strings.TrimSpace(fmt.Sprint(tool["type"])))
		switch {
		case isOpenAIWebSearchToolType(toolType):
			tools[i] = normalizeBigModelWebSearchTool(tool)
			changed = true
		case toolType == "mcp":
			tools[i] = normalizeBigModelMCPTool(tool)
			changed = true
		}
	}
	if changed {
		root["tools"] = tools
	}
	if toolChoice, ok := root["tool_choice"].(map[string]any); ok {
		choiceType := strings.ToLower(strings.TrimSpace(fmt.Sprint(toolChoice["type"])))
		switch {
		case isOpenAIWebSearchToolType(choiceType), choiceType == "mcp":
			delete(root, "tool_choice")
			changed = true
		}
	}
	if !changed {
		return payload, nil
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("normalize bigmodel tools: encode payload: %w", err)
	}
	return out, nil
}

func isBigModelCompatProvider(provider, baseURL string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))
	return strings.Contains(provider, "bigmodel") ||
		strings.Contains(provider, "zhipu") ||
		strings.Contains(baseURL, "open.bigmodel.cn")
}

func isOpenAIWebSearchToolType(toolType string) bool {
	return toolType == "web_search" || strings.HasPrefix(toolType, "web_search_")
}

func normalizeBigModelWebSearchTool(tool map[string]any) map[string]any {
	webSearch := objectValue(tool["web_search"])
	if webSearch == nil {
		webSearch = make(map[string]any)
	}
	if _, ok := webSearch["enable"]; !ok {
		webSearch["enable"] = true
	}
	if isEmptyJSONString(webSearch["search_engine"]) {
		webSearch["search_engine"] = "search_pro"
	}
	if _, ok := webSearch["content_size"]; !ok {
		if size := bigModelContentSize(fmt.Sprint(tool["search_context_size"])); size != "" {
			webSearch["content_size"] = size
		}
	}
	return map[string]any{
		"type":       "web_search",
		"web_search": webSearch,
	}
}

func normalizeBigModelMCPTool(tool map[string]any) map[string]any {
	mcp := objectValue(tool["mcp"])
	if mcp == nil {
		mcp = make(map[string]any)
	}
	for _, field := range []string{"server_label", "server_url", "transport_type", "allowed_tools", "headers"} {
		if _, ok := mcp[field]; !ok {
			if value, exists := tool[field]; exists {
				mcp[field] = value
			}
		}
	}
	if isEmptyJSONString(mcp["transport_type"]) {
		mcp["transport_type"] = "streamable-http"
	}
	return map[string]any{
		"type": "mcp",
		"mcp":  mcp,
	}
}

func objectValue(value any) map[string]any {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return obj
}

func isEmptyJSONString(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

func bigModelContentSize(searchContextSize string) string {
	switch strings.ToLower(strings.TrimSpace(searchContextSize)) {
	case "high":
		return "high"
	case "low", "medium":
		return "medium"
	default:
		return ""
	}
}

func imageEditPayloadHasUploads(payload []byte) bool {
	return gjson.GetBytes(payload, "image_files").Exists() || gjson.GetBytes(payload, "mask_file").Exists()
}

func buildImageEditsMultipartBody(payload []byte) ([]byte, string, error) {
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, "", fmt.Errorf("invalid image edits payload: %w", err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range root {
		switch key {
		case "image_files", "mask_file":
			continue
		}
		if value == nil {
			continue
		}
		fieldValue, err := stringifyMultipartField(value)
		if err != nil {
			return nil, "", fmt.Errorf("encode image edits field %s: %w", key, err)
		}
		if fieldValue == "" {
			continue
		}
		if err := writer.WriteField(key, fieldValue); err != nil {
			return nil, "", fmt.Errorf("write image edits field %s: %w", key, err)
		}
	}
	if err := writeImageEditFiles(writer, "image", root["image_files"]); err != nil {
		return nil, "", err
	}
	if err := writeImageEditFile(writer, "mask", root["mask_file"]); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("finalize image edits multipart body: %w", err)
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func stringifyMultipartField(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return fmt.Sprint(typed), nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case json.Number:
		return typed.String(), nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

func writeImageEditFiles(writer *multipart.Writer, fieldName string, value any) error {
	if value == nil {
		return nil
	}
	files, ok := value.([]any)
	if !ok {
		return fmt.Errorf("image edits %s must be an array", fieldName)
	}
	if len(files) == 0 {
		return fmt.Errorf("image edits %s is required", fieldName)
	}
	for _, file := range files {
		if err := writeImageEditFile(writer, fieldName, file); err != nil {
			return err
		}
	}
	return nil
}

func writeImageEditFile(writer *multipart.Writer, fieldName string, value any) error {
	if value == nil {
		return nil
	}
	file, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("image edits %s file must be an object", fieldName)
	}
	fileName := strings.TrimSpace(fmt.Sprint(file["file_name"]))
	if fileName == "" || fileName == "<nil>" {
		fileName = fieldName + ".png"
	}
	contentType := strings.TrimSpace(fmt.Sprint(file["content_type"]))
	if contentType == "" || contentType == "<nil>" {
		contentType = "application/octet-stream"
	}
	dataBase64 := strings.TrimSpace(fmt.Sprint(file["data_base64"]))
	if dataBase64 == "" || dataBase64 == "<nil>" {
		return fmt.Errorf("image edits %s file is missing data_base64", fieldName)
	}
	data, err := decodeImageEditBase64(dataBase64)
	if err != nil {
		return fmt.Errorf("decode image edits %s file: %w", fieldName, err)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeMultipartQuote(fieldName), escapeMultipartQuote(fileName)))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create image edits %s part: %w", fieldName, err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("write image edits %s file: %w", fieldName, err)
	}
	return nil
}

func decodeImageEditBase64(value string) ([]byte, error) {
	if comma := strings.Index(value, ","); comma >= 0 {
		prefix := strings.ToLower(strings.TrimSpace(value[:comma]))
		if !strings.HasPrefix(prefix, "data:") || !strings.Contains(prefix, ";base64") {
			return nil, fmt.Errorf("invalid data URI prefix in base64 content")
		}
		value = value[comma+1:]
	}
	return base64.StdEncoding.DecodeString(value)
}

func escapeMultipartQuote(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

func (e *OpenAICompatExecutor) applyCustomHeadersAndIdentityFingerprint(req *http.Request, auth *cliproxyauth.Auth, websocket bool, clientHeaders ...http.Header) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs, clientHeaders...)
	e.applyIdentityFingerprint(req, auth, websocket)
}

func (e *OpenAICompatExecutor) applyIdentityFingerprint(req *http.Request, auth *cliproxyauth.Auth, websocket bool) {
	if req == nil {
		return
	}
	fingerprint := ""
	if auth != nil && auth.Attributes != nil {
		fingerprint = strings.TrimSpace(strings.ToLower(auth.Attributes["identity_fingerprint"]))
	}
	if fingerprint == "" {
		if compat := e.resolveCompatConfig(auth); compat != nil {
			fingerprint = strings.TrimSpace(strings.ToLower(compat.IdentityFingerprint))
		}
	}
	switch fingerprint {
	case "codex":
		if fp, ok := codexIdentityFingerprint(e.cfg); ok {
			applyCodexIdentityFingerprintHeaders(req.Header, fp, websocket)
			if strings.TrimSpace(fp.Originator) != "" {
				req.Header.Set("Originator", fp.Originator)
			}
		}
	}
}
