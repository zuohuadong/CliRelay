// Package openai provides HTTP handlers for OpenAIResponses API endpoints.
// This package implements the OpenAIResponses-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAIResponses API requests to the appropriate backend format and
// convert responses back to OpenAIResponses-compatible format.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/optimize-multi-agent-v2"
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	responsesSSECommentKeepAliveFrame    = ": keep-alive\n\n"
	responsesSSEDataKeepAliveFrame       = "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n"
	responsesSSEKeepAliveDefaultInterval = 15 * time.Second
)

func writeResponsesSSEKeepAlive(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	frame := responsesSSECommentKeepAliveFrame
	if isCodexResponsesSSEClient(c) {
		frame = responsesSSEDataKeepAliveFrame
	}
	_, _ = io.WriteString(c.Writer, frame)
}

func isCodexResponsesSSEClient(c *gin.Context) bool {
	if c == nil {
		return false
	}
	originator := strings.ToLower(strings.TrimSpace(c.GetHeader("Originator")))
	switch originator {
	case "codex desktop", "codex_cli_rs", "codex-tui":
		return true
	}
	userAgent := strings.ToLower(strings.TrimSpace(c.GetHeader("User-Agent")))
	return strings.HasPrefix(userAgent, "codex desktop/") ||
		strings.HasPrefix(userAgent, "codex_cli_rs/") ||
		strings.HasPrefix(userAgent, "codex-tui/")
}

func responsesStreamKeepAliveInterval(h *OpenAIResponsesAPIHandler, c *gin.Context) time.Duration {
	if h != nil && h.BaseAPIHandler != nil {
		if interval := handlers.StreamingKeepAliveInterval(h.Cfg); interval > 0 {
			return interval
		}
	}
	if isCodexResponsesSSEClient(c) {
		return responsesSSEKeepAliveDefaultInterval
	}
	return 0
}

func writeResponsesSSEChunk(w io.Writer, chunk []byte) {
	if w == nil || len(chunk) == 0 {
		return
	}
	if _, err := w.Write(chunk); err != nil {
		return
	}
	if bytes.HasSuffix(chunk, []byte("\n\n")) || bytes.HasSuffix(chunk, []byte("\r\n\r\n")) {
		return
	}
	suffix := []byte("\n\n")
	if bytes.HasSuffix(chunk, []byte("\r\n")) {
		suffix = []byte("\r\n")
	} else if bytes.HasSuffix(chunk, []byte("\n")) {
		suffix = []byte("\n")
	}
	if _, err := w.Write(suffix); err != nil {
		return
	}
}

type responsesSSEFramer struct {
	pending              []byte
	outputItems          map[int][]byte
	outputOrder          []int
	unindexedOutputItems [][]byte
	completed            bool
	lastEvent            string
	terminalEvent        string
	terminalError        *interfaces.ErrorMessage
	failureEvent         string
	dataFrames           int
}

func (f *responsesSSEFramer) WriteChunk(w io.Writer, chunk []byte) {
	if len(chunk) == 0 || f.terminalEvent != "" {
		return
	}
	if responsesSSEStartsNewDataFrame(f.pending, chunk) {
		f.writeFrame(w, f.pending)
		f.pending = f.pending[:0]
		if f.terminalEvent != "" {
			return
		}
	}
	if responsesSSENeedsLineBreak(f.pending, chunk) {
		f.pending = append(f.pending, '\n')
	}
	f.pending = append(f.pending, chunk...)
	for {
		frameLen := responsesSSEFrameLen(f.pending)
		if frameLen == 0 {
			break
		}
		f.writeFrame(w, f.pending[:frameLen])
		copy(f.pending, f.pending[frameLen:])
		f.pending = f.pending[:len(f.pending)-frameLen]
		if f.terminalEvent != "" {
			f.pending = f.pending[:0]
			return
		}
	}
	if len(bytes.TrimSpace(f.pending)) == 0 {
		f.pending = f.pending[:0]
		return
	}
	if len(f.pending) == 0 || !responsesSSECanEmitWithoutDelimiter(f.pending) {
		return
	}
	f.writeFrame(w, f.pending)
	f.pending = f.pending[:0]
}

func (f *responsesSSEFramer) Flush(w io.Writer) {
	if len(f.pending) == 0 || f.terminalEvent != "" {
		return
	}
	if len(bytes.TrimSpace(f.pending)) == 0 {
		f.pending = f.pending[:0]
		return
	}
	if !responsesSSECanFlushWithoutDelimiter(f.pending) {
		f.pending = f.pending[:0]
		return
	}
	f.writeFrame(w, f.pending)
	f.pending = f.pending[:0]
}

func (f *responsesSSEFramer) WriteDone(w io.Writer) {
	f.Flush(w)
	if f.completed {
		_, _ = w.Write([]byte("\n"))
		return
	}
	payload := buildResponsesTerminalCompletedPayload(f.completedOutputPayload())
	writeResponsesSSEChunk(w, responsesSSEFrameWithData(nil, payload))
}

func (f *responsesSSEFramer) WriteTerminalError(w io.Writer, status int, errText string) {
	f.Flush(w)
	if handlers.IsOpenAIResponsesContextWindowError(status, errText) || isResponsesServiceUnavailableTerminalError(status, errText) {
		failedPayload := handlers.BuildOpenAIResponsesResponseFailedChunk(status, errText, 0)
		writeResponsesSSEChunk(w, responsesSSEFrameWithData([]byte("event: response.failed\n"), failedPayload))
		return
	}
	itemPayload, completedPayload := buildResponsesTerminalErrorPayloads(status, errText)
	writeResponsesSSEChunk(w, responsesSSEFrameWithData([]byte("event: response.output_item.done\n"), itemPayload))
	writeResponsesSSEChunk(w, responsesSSEFrameWithData([]byte("event: response.completed\n"), completedPayload))
}

func (f *responsesSSEFramer) writeFrame(w io.Writer, frame []byte) {
	writeResponsesSSEChunk(w, f.repairFrame(frame))
}

func (f *responsesSSEFramer) repairFrame(frame []byte) []byte {
	payload, ok := responsesSSEDataPayload(frame)
	if !ok || len(payload) == 0 {
		return frame
	}
	if bytes.Equal(payload, []byte("[DONE]")) {
		f.dataFrames++
		return frame
	}
	if !json.Valid(payload) {
		return frame
	}
	f.dataFrames++

	payloadType := gjson.GetBytes(payload, "type").String()
	if responsesSSEErrorEvent(payloadType) || responsesSSEPayloadHasError(payload) {
		if payloadType != "" {
			f.lastEvent = sanitizeResponsesStreamEventName(payloadType)
		}
		return f.repairErrorPayload(payload)
	}
	streamEvent := responsesSSEEventName(frame)
	eventType := payloadType
	if responsesSSETerminalEvent(streamEvent) {
		eventType = streamEvent
	} else if eventType == "" {
		eventType = streamEvent
	}
	if eventType != "" {
		f.lastEvent = sanitizeResponsesStreamEventName(eventType)
	}
	if responsesSSEErrorEvent(eventType) {
		return f.repairErrorPayload(payload)
	}
	if responsesSSETerminalEvent(eventType) {
		f.terminalEvent = eventType
	}

	switch eventType {
	case "response.output_item.done":
		f.recordOutputItem(payload)
	case "response.completed":
		f.completed = true
		repaired := f.repairCompletedPayload(payload)
		if !bytes.Equal(repaired, payload) {
			return responsesSSEFrameWithData(frame, repaired)
		}
	}
	return frame
}

func responsesSSEPayloadErrorMessage(payload []byte) *interfaces.ErrorMessage {
	status := http.StatusBadGateway
	for _, path := range []string{"status", "status_code", "error.status", "error.status_code", "response.error.status", "response.error.status_code"} {
		candidate := int(gjson.GetBytes(payload, path).Int())
		if candidate >= http.StatusBadRequest && candidate <= 599 {
			status = candidate
			break
		}
	}
	return sanitizeResponsesStreamErrorMessage(&interfaces.ErrorMessage{StatusCode: status, Error: fmt.Errorf("%s", payload)})
}

func (f *responsesSSEFramer) repairErrorPayload(payload []byte) []byte {
	errMsg := responsesSSEPayloadErrorMessage(payload)
	status := errMsg.StatusCode
	f.terminalError = errMsg
	failureEvent := f.failureEvent
	if failureEvent != "response.failed" {
		failureEvent = "error"
	}
	f.terminalEvent = failureEvent
	errText := responsesStreamErrorText(errMsg, status)
	if failureEvent == "response.failed" {
		chunk := handlers.BuildOpenAIResponsesStreamFailedChunk(status, errText, 0)
		return []byte(fmt.Sprintf("event: response.failed\ndata: %s\n\n", chunk))
	}
	chunk := handlers.BuildOpenAIResponsesStreamErrorChunk(status, errText, 0)
	return []byte(fmt.Sprintf("event: error\ndata: %s\n\n", chunk))
}

func responsesSSEErrorEvent(eventType string) bool {
	switch eventType {
	case "response.failed", "response.error", "error":
		return true
	default:
		return false
	}
}

func responsesSSETerminalEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.incomplete", "response.failed", "response.done", "response.error", "error":
		return true
	default:
		return false
	}
}

func responsesSSEPayloadHasError(payload []byte) bool {
	for _, path := range []string{"error", "response.error"} {
		result := gjson.GetBytes(payload, path)
		if result.Exists() && result.Type != gjson.Null {
			return true
		}
	}
	return gjson.GetBytes(payload, "code").Exists() && gjson.GetBytes(payload, "message").Exists()
}

func responsesSSEDataPayload(frame []byte) ([]byte, bool) {
	var payload []byte
	found := false
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(trimmed[len("data:"):])
		if found {
			payload = append(payload, '\n')
		}
		payload = append(payload, data...)
		found = true
	}
	return payload, found
}

func responsesSSEFrameWithData(frame, payload []byte) []byte {
	var out bytes.Buffer
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	for _, line := range bytes.Split(payload, []byte("\n")) {
		out.WriteString("data: ")
		out.Write(line)
		out.WriteByte('\n')
	}
	out.WriteByte('\n')
	return out.Bytes()
}

func (f *responsesSSEFramer) recordOutputItem(payload []byte) {
	item := gjson.GetBytes(payload, "item")
	if !item.Exists() || !item.IsObject() || item.Get("type").String() == "" {
		return
	}

	if outputIndex := gjson.GetBytes(payload, "output_index"); outputIndex.Exists() {
		index := int(outputIndex.Int())
		if f.outputItems == nil {
			f.outputItems = make(map[int][]byte)
		}
		if _, exists := f.outputItems[index]; !exists {
			f.outputOrder = append(f.outputOrder, index)
		}
		f.outputItems[index] = append([]byte(nil), item.Raw...)
		return
	}

	f.unindexedOutputItems = append(f.unindexedOutputItems, append([]byte(nil), item.Raw...))
}

func (f *responsesSSEFramer) repairCompletedPayload(payload []byte) []byte {
	if len(f.outputOrder) == 0 && len(f.unindexedOutputItems) == 0 {
		return payload
	}
	output := gjson.GetBytes(payload, "response.output")
	if output.Exists() && (!output.IsArray() || len(output.Array()) > 0) {
		return payload
	}

	outputJSON := f.completedOutputPayload()
	repaired, err := sjson.SetRawBytes(payload, "response.output", outputJSON)
	if err != nil {
		return payload
	}
	return repaired
}

func (f *responsesSSEFramer) completedOutputPayload() []byte {
	if f == nil || (len(f.outputOrder) == 0 && len(f.unindexedOutputItems) == 0) {
		return []byte("[]")
	}
	var outputJSON bytes.Buffer
	outputJSON.WriteByte('[')
	indexes := append([]int(nil), f.outputOrder...)
	sort.Ints(indexes)
	written := 0
	for _, index := range indexes {
		item, ok := f.outputItems[index]
		if !ok {
			continue
		}
		if written > 0 {
			outputJSON.WriteByte(',')
		}
		outputJSON.Write(item)
		written++
	}
	for _, item := range f.unindexedOutputItems {
		if written > 0 {
			outputJSON.WriteByte(',')
		}
		outputJSON.Write(item)
		written++
	}
	outputJSON.WriteByte(']')
	return outputJSON.Bytes()
}

func buildResponsesTerminalCompletedPayload(output []byte) []byte {
	if !gjson.ParseBytes(output).IsArray() {
		output = []byte("[]")
	}
	payload := []byte(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	var errSet error
	payload, errSet = sjson.SetBytes(payload, "response.created_at", time.Now().Unix())
	if errSet != nil {
		return []byte(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	}
	payload, errSet = sjson.SetRawBytes(payload, "response.output", output)
	if errSet != nil {
		return []byte(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	}
	return payload
}

func buildResponsesTerminalErrorPayloads(status int, errText string) ([]byte, []byte) {
	failedPayload := handlers.BuildOpenAIResponsesResponseFailedChunk(status, errText, 0)
	code := strings.TrimSpace(gjson.GetBytes(failedPayload, "response.error.code").String())
	errType := strings.TrimSpace(gjson.GetBytes(failedPayload, "response.error.type").String())
	message := strings.TrimSpace(gjson.GetBytes(failedPayload, "response.error.message").String())
	if message == "" {
		message = strings.TrimSpace(errText)
	}
	if message == "" {
		message = http.StatusText(status)
	}

	displayText := message
	if code != "" {
		displayText = fmt.Sprintf("Upstream request failed (%s): %s", code, message)
	}
	itemID := fmt.Sprintf("msg_error_%d", time.Now().UnixNano())
	item := map[string]any{
		"type":  "message",
		"id":    itemID,
		"role":  "assistant",
		"phase": "final_answer",
		"content": []map[string]string{{
			"type": "output_text",
			"text": displayText,
		}},
	}
	itemPayload := map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": 0,
		"output_index":    0,
		"item":            item,
	}
	output := []any{item}
	if code != "" || errType != "" {
		itemPayload["metadata"] = map[string]string{
			"error_code": code,
			"error_type": errType,
		}
	}

	itemPayloadJSON, err := json.Marshal(itemPayload)
	if err != nil || len(itemPayloadJSON) == 0 {
		itemPayloadJSON = []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Upstream request failed."}],"phase":"final_answer"}}`)
	}
	outputJSON, err := json.Marshal(output)
	if err != nil || len(outputJSON) == 0 {
		outputJSON = []byte(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Upstream request failed."}],"phase":"final_answer"}]`)
	}
	completedPayload := buildResponsesTerminalCompletedPayload(outputJSON)
	if code != "" {
		if updated, errSet := sjson.SetBytes(completedPayload, "response.metadata.error_code", code); errSet == nil {
			completedPayload = updated
		}
	}
	if errType != "" {
		if updated, errSet := sjson.SetBytes(completedPayload, "response.metadata.error_type", errType); errSet == nil {
			completedPayload = updated
		}
	}
	return itemPayloadJSON, completedPayload
}

func isResponsesServiceUnavailableTerminalError(status int, errText string) bool {
	failedPayload := handlers.BuildOpenAIResponsesResponseFailedChunk(status, errText, 0)
	code := strings.TrimSpace(gjson.GetBytes(failedPayload, "response.error.code").String())
	errType := strings.TrimSpace(gjson.GetBytes(failedPayload, "response.error.type").String())
	return status == http.StatusServiceUnavailable ||
		strings.EqualFold(code, "server_is_overloaded") ||
		strings.EqualFold(errType, "service_unavailable_error")
}

func responsesSSEFrameLen(chunk []byte) int {
	if len(chunk) == 0 {
		return 0
	}
	lf := bytes.Index(chunk, []byte("\n\n"))
	crlf := bytes.Index(chunk, []byte("\r\n\r\n"))
	switch {
	case lf < 0:
		if crlf < 0 {
			return 0
		}
		return crlf + 4
	case crlf < 0:
		return lf + 2
	case lf < crlf:
		return lf + 2
	default:
		return crlf + 4
	}
}

func responsesSSENeedsMoreData(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return false
	}
	return responsesSSEHasField(trimmed, []byte("event:")) && !responsesSSEHasField(trimmed, []byte("data:"))
}

func responsesSSEHasField(chunk []byte, prefix []byte) bool {
	s := chunk
	for len(s) > 0 {
		line := s
		if i := bytes.IndexByte(s, '\n'); i >= 0 {
			line = s[:i]
			s = s[i+1:]
		} else {
			s = nil
		}
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func responsesSSECanEmitWithoutDelimiter(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 || responsesSSENeedsMoreData(trimmed) ||
		!responsesSSEHasField(trimmed, []byte("event:")) || !responsesSSEHasField(trimmed, []byte("data:")) {
		return false
	}
	return responsesSSEDataLinesValid(trimmed)
}

func responsesSSECanFlushWithoutDelimiter(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	return len(trimmed) > 0 && responsesSSEHasField(trimmed, []byte("data:")) && responsesSSEDataLinesValid(trimmed)
}

func responsesSSEStartsNewDataFrame(pending, chunk []byte) bool {
	trimmedPending := bytes.TrimSpace(pending)
	if len(trimmedPending) == 0 || responsesSSEHasField(trimmedPending, []byte("event:")) ||
		!responsesSSEHasField(trimmedPending, []byte("data:")) || !responsesSSEDataLinesValid(trimmedPending) {
		return false
	}
	trimmedChunk := bytes.TrimLeft(chunk, " \t\r\n")
	return bytes.HasPrefix(trimmedChunk, []byte("data:"))
}

func responsesSSEEventName(frame []byte) string {
	for _, line := range bytes.Split(frame, []byte("\n")) {
		trimmed := bytes.TrimSpace(bytes.TrimRight(line, "\r"))
		if bytes.HasPrefix(trimmed, []byte("event:")) {
			return strings.TrimSpace(string(trimmed[len("event:"):]))
		}
	}
	return ""
}

func responsesSSEDataLinesValid(chunk []byte) bool {
	payload, found := responsesSSEDataPayload(chunk)
	if !found {
		return true
	}
	payload = bytes.TrimSpace(payload)
	return len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || json.Valid(payload)
}

func responsesSSENeedsLineBreak(pending, chunk []byte) bool {
	if len(pending) == 0 || len(chunk) == 0 {
		return false
	}
	if bytes.HasSuffix(pending, []byte("\n")) || bytes.HasSuffix(pending, []byte("\r")) {
		return false
	}
	if chunk[0] == '\n' || chunk[0] == '\r' {
		return false
	}
	trimmed := bytes.TrimLeft(chunk, " \t")
	if len(trimmed) == 0 {
		return false
	}
	for _, prefix := range [][]byte{[]byte("data:"), []byte("event:"), []byte("id:"), []byte("retry:"), []byte(":")} {
		if bytes.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// OpenAIResponsesAPIHandler contains the handlers for OpenAIResponses API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIResponsesAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewOpenAIResponsesAPIHandler creates a new OpenAIResponses API handlers instance.
// It takes an BaseAPIHandler instance as input and returns an OpenAIResponsesAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIResponsesAPIHandler: A new OpenAIResponses API handlers instance
func NewOpenAIResponsesAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIResponsesAPIHandler {
	return &OpenAIResponsesAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIResponsesAPIHandler) HandlerType() string {
	return OpenaiResponse
}

// Models returns the OpenAIResponses-compatible model metadata supported by this handler.
func (h *OpenAIResponsesAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// rejectUnconfiguredModel checks if the model is configured when RejectUnconfiguredModels is enabled.
// Returns true if the request was rejected (model not configured), false otherwise.
func (h *OpenAIResponsesAPIHandler) rejectUnconfiguredModel(c *gin.Context, modelName string) bool {
	if !h.BaseAPIHandler.Cfg.RejectUnconfiguredModels {
		return false
	}
	if modelName == "" {
		return false
	}
	if registry.GetGlobalRegistry().IsModelConfigured(modelName) {
		return false
	}
	c.JSON(http.StatusNotFound, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: fmt.Sprintf("Model %s is not configured. No auth credentials are available for this model.", modelName),
			Type:    "invalid_request_error",
		},
	})
	return true
}

// OpenAIResponsesModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAIResponses-compatible format.
func (h *OpenAIResponsesAPIHandler) OpenAIResponsesModels(c *gin.Context) {
	models := h.Models()
	models = h.BaseAPIHandler.FilterModelsByAccess(c, models)
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

func (h *OpenAIResponsesAPIHandler) prepareCodexMultiAgentV2Tools(c *gin.Context, payload []byte) []byte {
	if h == nil || h.Cfg == nil {
		return payload
	}

	requestCtx := context.Background()
	if c != nil && c.Request != nil {
		requestCtx = c.Request.Context()
	}
	requestCtx = context.WithValue(requestCtx, "gin", c)

	var requestHeaders http.Header
	if c != nil && c.Request != nil {
		requestHeaders = c.Request.Header
	}
	homeEnabled := h.AuthManager != nil && h.AuthManager.HomeEnabled()
	updated, prepared := multiagentv2.PrepareCodexMultiAgentV2Tools(
		requestCtx,
		requestHeaders,
		payload,
		h.Cfg.CodexOptimizeMultiAgentV2,
		homeEnabled,
	)
	if prepared && c != nil {
		c.Set(multiagentv2.CodexMultiAgentV2ToolsPreparedContextKey, true)
	}
	return updated
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBodyWithLimit(c, h.responsesMaxInboundBytes())
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		if errors.Is(err, handlers.ErrRequestBodyTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, handlers.ErrorResponse{Error: handlers.ErrorDetail{
				Message: "request body exceeds the configured size limit",
				Type:    "invalid_request_error",
				Code:    "request_body_too_large",
			}})
			return
		}
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	modelName := gjson.GetBytes(rawJSON, "model").String()
	if h.rejectUnconfiguredModel(c, modelName) {
		return
	}
	rawJSON = h.prepareCodexMultiAgentV2Tools(c, rawJSON)

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

func (h *OpenAIResponsesAPIHandler) Compact(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBodyWithLimit(c, h.responsesMaxInboundBytes())
	if err != nil {
		if errors.Is(err, handlers.ErrRequestBodyTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, handlers.ErrorResponse{Error: handlers.ErrorDetail{
				Message: "request body exceeds the configured size limit",
				Type:    "invalid_request_error",
				Code:    "request_body_too_large",
			}})
			return
		}
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	modelName := gjson.GetBytes(rawJSON, "model").String()
	if h.rejectUnconfiguredModel(c, modelName) {
		return
	}
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported for compact responses",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if streamResult.Exists() {
		if updated, err := sjson.DeleteBytes(rawJSON, "stream"); err == nil {
			rawJSON = updated
		}
	}

	c.Header("Content-Type", "application/json")
	modelName = gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "responses/compact")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func (h *OpenAIResponsesAPIHandler) responsesMaxInboundBytes() int64 {
	if h != nil && h.BaseAPIHandler != nil && h.Cfg != nil && h.Cfg.ResponsesMaxInboundBytes > 0 {
		return h.Cfg.ResponsesMaxInboundBytes
	}
	return sdkconfig.DefaultResponsesMaxInboundBytes
}

// handleNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAIResponses format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// Get the http.Flusher interface to manually flush the response.
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	// New core execution path
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}
	failureEvent := "error"
	if isCodexResponsesClientRequest(c) {
		failureEvent = "response.failed"
	}
	framer := &responsesSSEFramer{failureEvent: failureEvent}
	var initialOutput bytes.Buffer

	setSSEHeaders()
	stopBootstrapHeartbeat := func() {}
	if h.AuthManager == nil || !h.AuthManager.HomeEnabled() {
		stopBootstrapHeartbeat = h.startResponsesStreamBootstrapHeartbeat(c, flusher)
	}
	dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	stopBootstrapHeartbeat()
	// Peek at the first complete SSE data frame.
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// Err channel closed cleanly; wait for data channel.
				errChan = nil
				continue
			}
			framer.Flush(&initialOutput)
			safeErrMsg := sanitizeResponsesStreamErrorMessage(errMsg)
			if framer.dataFrames == 0 {
				safeErrMsg = sanitizeResponsesInitialErrorMessage(errMsg)
			}
			if safeErrMsg != nil && framer.dataFrames > 0 {
				setSSEHeaders()
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				_, _ = c.Writer.Write(initialOutput.Bytes())
				flusher.Flush()
				pendingErrors := make(chan *interfaces.ErrorMessage, 1)
				pendingErrors <- safeErrMsg
				close(pendingErrors)
				h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors, framer)
				return
			}
			// Upstream failed before a complete SSE data frame. Return JSON.
			h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), safeErrMsg)
			h.WriteErrorResponse(c, safeErrMsg)
			if safeErrMsg != nil {
				cliCancel(safeErrMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				framer.Flush(&initialOutput)
				errMsg, hasPendingError := handlers.PendingStreamError(errChan)
				if !hasPendingError && framer.terminalEvent == "" {
					message := "upstream stream closed before first payload"
					if framer.dataFrames > 0 {
						message = "upstream stream closed before a terminal event"
					}
					errMsg = &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("%s", message)}
				}
				if framer.dataFrames > 0 {
					errMsg = sanitizeResponsesStreamErrorMessage(errMsg)
				} else {
					errMsg = sanitizeResponsesInitialErrorMessage(errMsg)
				}

				if framer.dataFrames > 0 {
					setSSEHeaders()
					handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
					_, _ = c.Writer.Write(initialOutput.Bytes())
					flusher.Flush()
					if framer.terminalError != nil {
						h.logResponsesStreamError(c, framer, framer.terminalError)
						cliCancel(framer.terminalError.Error)
						return
					}
					if errMsg == nil {
						cliCancel(nil)
						return
					}
					pendingErrors := make(chan *interfaces.ErrorMessage, 1)
					pendingErrors <- errMsg
					close(pendingErrors)
					h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors, framer)
					return
				}

				h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
				h.WriteErrorResponse(c, errMsg)
				if errMsg != nil {
					cliCancel(errMsg.Error)
				} else {
					cliCancel(nil)
				}
				return
			}

			framer.WriteChunk(&initialOutput, chunk)
			if framer.dataFrames == 0 {
				continue
			}

			setSSEHeaders()
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
			_, _ = c.Writer.Write(initialOutput.Bytes())
			flusher.Flush()
			if framer.terminalError != nil {
				h.logResponsesStreamError(c, framer, framer.terminalError)
				cliCancel(framer.terminalError.Error)
				return
			}

			h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, framer)
			return
		}
	}
}

func (h *OpenAIResponsesAPIHandler) startResponsesStreamBootstrapHeartbeat(c *gin.Context, flusher http.Flusher) func() {
	if c == nil || flusher == nil {
		return func() {}
	}
	writeHeartbeat := func() {
		writeResponsesSSEKeepAlive(c)
		flusher.Flush()
	}
	interval := responsesStreamKeepAliveInterval(h, c)
	if interval <= 0 {
		return func() {}
	}

	stopChan := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-c.Request.Context().Done():
				return
			case <-ticker.C:
				writeHeartbeat()
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(stopChan)
		})
		wg.Wait()
	}
}

func isCodexResponsesClientRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	if multiagentv2.IsCodexClientUserAgent(c.GetHeader("User-Agent")) {
		return true
	}

	switch originator := strings.ToLower(strings.TrimSpace(c.GetHeader("Originator"))); originator {
	case "codex desktop", "codex-tui", "codex_cli_rs":
		return true
	default:
		return strings.HasPrefix(originator, "codex desktop/") || strings.HasPrefix(originator, "codex-tui/") || strings.HasPrefix(originator, "codex_cli_rs/")
	}
}

const (
	responsesStreamErrorMessageLimit = 2048
	responsesStreamErrorFieldLimit   = 256
)

var (
	responsesStreamSensitiveValuePattern = regexp.MustCompile(`(?i)((?:"?(?:api[_-]?key|access[_-]?token|token|authorization|secret)"?)\s*[=:]\s*"?)([^\s"&,;}]+)`)
	responsesStreamBearerPattern         = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
)

func truncateResponsesStreamErrorText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func redactResponsesStreamErrorText(text string) string {
	text = responsesStreamSensitiveValuePattern.ReplaceAllString(text, `${1}[REDACTED]`)
	return responsesStreamBearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
}

func sanitizeResponsesStreamEventName(eventName string) string {
	return truncateResponsesStreamErrorText(redactResponsesStreamErrorText(strings.TrimSpace(eventName)), responsesStreamErrorFieldLimit)
}

func responsesStreamErrorText(errMsg *interfaces.ErrorMessage, status int) string {
	text := http.StatusText(status)
	if errMsg != nil && errMsg.Error != nil && strings.TrimSpace(errMsg.Error.Error()) != "" {
		text = strings.TrimSpace(errMsg.Error.Error())
	}
	if !json.Valid([]byte(text)) {
		return truncateResponsesStreamErrorText(redactResponsesStreamErrorText(text), responsesStreamErrorMessageLimit)
	}

	root := gjson.Parse(text)
	errorNode := root.Get("error")
	if !errorNode.Exists() || !errorNode.IsObject() {
		errorNode = root.Get("response.error")
	}
	if errorNode.Exists() && errorNode.IsObject() {
		safe := []byte(`{"error":{}}`)
		copied := false
		for _, field := range []string{"type", "code", "message", "param"} {
			value := errorNode.Get(field)
			if !value.Exists() || value.Type == gjson.Null {
				continue
			}
			limit := responsesStreamErrorFieldLimit
			if field == "message" {
				limit = responsesStreamErrorMessageLimit
			}
			safe, _ = sjson.SetBytes(safe, "error."+field, truncateResponsesStreamErrorText(redactResponsesStreamErrorText(value.String()), limit))
			copied = true
		}
		if copied {
			return string(safe)
		}
	}

	safe := []byte(`{"type":"error"}`)
	copied := false
	for _, field := range []string{"code", "message", "param"} {
		value := root.Get(field)
		if !value.Exists() || value.Type == gjson.Null {
			continue
		}
		limit := responsesStreamErrorFieldLimit
		if field == "message" {
			limit = responsesStreamErrorMessageLimit
		}
		safe, _ = sjson.SetBytes(safe, field, truncateResponsesStreamErrorText(redactResponsesStreamErrorText(value.String()), limit))
		copied = true
	}
	if copied {
		return string(safe)
	}
	return http.StatusText(status)
}

type responsesStreamSanitizedError struct {
	message string
	cause   error
}

func (e *responsesStreamSanitizedError) Error() string { return e.message }
func (e *responsesStreamSanitizedError) Unwrap() error { return e.cause }

func sanitizeResponsesInitialErrorMessage(errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if errMsg != nil && errMsg.DirectResponse {
		return errMsg
	}
	return sanitizeResponsesStreamErrorMessage(errMsg)
}

func sanitizeResponsesStreamErrorMessage(errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if errMsg == nil {
		return nil
	}
	status := errMsg.StatusCode
	if status < http.StatusBadRequest || status > 599 {
		status = http.StatusInternalServerError
	}
	safe := *errMsg
	safe.StatusCode = status
	safe.Error = &responsesStreamSanitizedError{message: responsesStreamErrorText(errMsg, status), cause: errMsg.Error}
	safe.DirectResponse = false
	safe.Body = nil
	return &safe
}

func (h *OpenAIResponsesAPIHandler) logResponsesStreamError(c *gin.Context, framer *responsesSSEFramer, errMsg *interfaces.ErrorMessage) {
	if errMsg == nil {
		return
	}
	status := errMsg.StatusCode
	if status < http.StatusBadRequest || status > 599 {
		status = http.StatusInternalServerError
	}
	lastEvent := "none"
	if framer != nil && framer.lastEvent != "" {
		lastEvent = framer.lastEvent
	}
	errText := responsesStreamErrorText(errMsg, status)
	h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), &interfaces.ErrorMessage{
		StatusCode: status,
		Error:      fmt.Errorf("responses stream terminated after %s: %s", lastEvent, errText),
	})
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, framer *responsesSSEFramer) {
	if framer == nil {
		framer = &responsesSSEFramer{}
	}
	keepAliveInterval := responsesStreamKeepAliveInterval(h, c)
	if isCodexResponsesClientRequest(c) {
		framer.failureEvent = "response.failed"
	} else {
		framer.failureEvent = "error"
	}
	writeTerminalError := func(errMsg *interfaces.ErrorMessage) {
		framer.Flush(c.Writer)
		if errMsg == nil {
			return
		}
		status := http.StatusInternalServerError
		if errMsg.StatusCode > 0 {
			status = errMsg.StatusCode
		}
		errText := responsesStreamErrorText(errMsg, status)
		h.logResponsesStreamError(c, framer, errMsg)
		if framer.terminalEvent != "" {
			return
		}
		if isCodexResponsesClientRequest(c) {
			chunk := handlers.BuildOpenAIResponsesStreamFailedChunk(status, errText, 0)
			_, _ = fmt.Fprintf(c.Writer, "\nevent: response.failed\ndata: %s\n\n", string(chunk))
			return
		}
		chunk := handlers.BuildOpenAIResponsesStreamErrorChunk(status, errText, 0)
		if flattened, errDelete := sjson.DeleteBytes(chunk, "error"); errDelete == nil {
			chunk = flattened
		}
		_, _ = fmt.Fprintf(c.Writer, "\nevent: error\ndata: %s\n\n", string(chunk))
	}

	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		KeepAliveInterval:      &keepAliveInterval,
		NormalizeTerminalError: sanitizeResponsesStreamErrorMessage,
		WriteChunk: func(chunk []byte) {
			framer.WriteChunk(c.Writer, chunk)
		},
		ChunkError: func() *interfaces.ErrorMessage {
			if framer.terminalError != nil {
				h.logResponsesStreamError(c, framer, framer.terminalError)
			}
			return framer.terminalError
		},
		WriteTerminalError: writeTerminalError,
		CloseError: func() *interfaces.ErrorMessage {
			framer.Flush(c.Writer)
			if framer.terminalError != nil {
				return framer.terminalError
			}
			if framer.terminalEvent != "" {
				return nil
			}
			lastEvent := framer.lastEvent
			if lastEvent == "" {
				lastEvent = "none"
			}
			return &interfaces.ErrorMessage{
				StatusCode: http.StatusBadGateway,
				Error:      fmt.Errorf("upstream stream closed before a terminal event (last event: %s)", lastEvent),
			}
		},
		WriteDone: func() {
			framer.WriteDone(c.Writer)
		},
		WriteKeepAlive: func() {
			writeResponsesSSEKeepAlive(c)
		},
	})
}

func responsesErrorStatusAndText(errMsg *interfaces.ErrorMessage) (int, string) {
	status := http.StatusInternalServerError
	if errMsg != nil && errMsg.StatusCode > 0 {
		status = errMsg.StatusCode
	}
	errText := http.StatusText(status)
	if errMsg != nil && errMsg.Error != nil && errMsg.Error.Error() != "" {
		errText = errMsg.Error.Error()
	}
	return status, errText
}
