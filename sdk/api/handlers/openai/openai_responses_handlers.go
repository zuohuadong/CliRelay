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
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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

// OpenAIResponsesModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAIResponses-compatible format.
func (h *OpenAIResponsesAPIHandler) OpenAIResponsesModels(c *gin.Context) {
	models := h.Models()
	if isCherryStudioRequest(c) {
		rewritten := make([]map[string]any, 0, len(models))
		for _, model := range models {
			if model == nil {
				continue
			}
			cloned := make(map[string]any, len(model))
			for key, value := range model {
				cloned[key] = value
			}
			if modelID, _ := cloned["id"].(string); modelID != "" {
				cloned["id"] = presentedImageModelID(c, modelID)
			}
			rewritten = append(rewritten, cloned)
		}
		models = rewritten
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
	rawJSON, ok := handlers.ReadJSONRequestBody(c)
	if !ok {
		return
	}
	rawJSON = normalizeImageAliasPayload(rawJSON)
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String()) == openAIImageModelID {
		h.handleImageResponse(c, rawJSON)
		return
	}

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

func normalizeImageAliasPayload(rawJSON []byte) []byte {
	modelName := normalizeImageModelAlias(gjson.GetBytes(rawJSON, "model").String())
	if modelName == strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String()) {
		return rawJSON
	}
	updated, err := sjson.SetBytes(rawJSON, "model", modelName)
	if err != nil {
		return rawJSON
	}
	return updated
}

func (h *OpenAIResponsesAPIHandler) handleImageResponse(c *gin.Context, rawJSON []byte) {
	prompt := openAIResponsesImagePrompt(rawJSON)
	if prompt == "" {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("gpt-image-2 responses requests require text input"),
		})
		return
	}

	imagePayload, err := openAIResponsesImagePayload(rawJSON, prompt)
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: err})
		return
	}

	stream := gjson.GetBytes(rawJSON, "stream").Bool()
	if stream {
		h.handleStreamingImageResponse(c, rawJSON, imagePayload)
		return
	}
	h.handleNonStreamingImageResponse(c, rawJSON, imagePayload)
}

func openAIResponsesImagePrompt(rawJSON []byte) string {
	parts := make([]string, 0, 4)
	appendText := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" || text == "[undefined]" {
			return
		}
		parts = append(parts, text)
	}

	if instructions := gjson.GetBytes(rawJSON, "instructions"); instructions.Type == gjson.String {
		appendText(instructions.String())
	}

	input := gjson.GetBytes(rawJSON, "input")
	if input.Type == gjson.String {
		appendText(input.String())
	} else if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			content := item.Get("content")
			if content.Type == gjson.String {
				appendText(content.String())
				return true
			}
			if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					partType := strings.TrimSpace(part.Get("type").String())
					if partType == "" || partType == "input_text" || partType == "output_text" || partType == "text" {
						appendText(part.Get("text").String())
					}
					return true
				})
			}
			return true
		})
	}

	return strings.Join(parts, "\n\n")
}

func openAIResponsesImagePayload(rawJSON []byte, prompt string) ([]byte, error) {
	payload := map[string]any{
		"model":  openAIImageModelID,
		"prompt": prompt,
	}
	for _, key := range []string{"size", "quality", "background", "moderation", "output_compression", "output_format", "response_format", "n"} {
		value := gjson.GetBytes(rawJSON, key)
		if !value.Exists() || value.Type == gjson.Null {
			continue
		}
		if value.Type == gjson.String && strings.TrimSpace(value.String()) == "[undefined]" {
			continue
		}
		payload[key] = value.Value()
	}
	return json.Marshal(payload)
}

func (h *OpenAIResponsesAPIHandler) executeImageResponse(c *gin.Context, rawJSON, imagePayload []byte) ([]byte, http.Header, error) {
	if h.AuthManager == nil {
		return nil, nil, fmt.Errorf("authentication manager not initialized")
	}
	cliCtx := context.WithValue(c.Request.Context(), util.ContextKeyGin, c)
	resp, err := h.AuthManager.Execute(cliCtx, []string{"codex"}, coreexecutor.Request{
		Model:   "",
		Payload: imagePayload,
		Format:  sdktranslator.FromString("openai"),
	}, coreexecutor.Options{
		Alt:             openAIImageGenerationAlt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString("openai"),
		Metadata:        cloneImageExecutionMetadata(requestImageExecutionMetadata(c)),
	})
	if err != nil {
		return nil, nil, err
	}
	return resp.Payload, resp.Headers, nil
}

func (h *OpenAIResponsesAPIHandler) handleNonStreamingImageResponse(c *gin.Context, rawJSON, imagePayload []byte) {
	c.Header("Content-Type", "application/json")
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, c.Request.Context())
	payload, upstreamHeaders, err := h.executeImageResponse(c, rawJSON, imagePayload)
	stopKeepAlive()
	if err != nil {
		h.writeImageResponseExecutionError(c, err)
		return
	}
	text, err := openAIResponsesImageMarkdown(payload)
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err})
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	c.Data(http.StatusOK, "application/json; charset=utf-8", openAIResponsesImageJSON(text))
}

func (h *OpenAIResponsesAPIHandler) handleStreamingImageResponse(c *gin.Context, rawJSON, imagePayload []byte) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: "Streaming not supported", Type: "server_error"}})
		return
	}
	handlers.PrepareStreamingResponse(c)
	responseID := openAIResponsesImageResponseID()
	createdAt := time.Now().Unix()
	openAIResponsesWriteSSE(c, flusher, "response.created", openAIResponsesImageEvent("response.created", 0, responseID, createdAt, "", false))
	openAIResponsesWriteSSE(c, flusher, "response.in_progress", openAIResponsesImageEvent("response.in_progress", 1, responseID, createdAt, "", false))

	payload, upstreamHeaders, err := h.executeImageResponse(c, rawJSON, imagePayload)
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	if err != nil {
		status := http.StatusBadGateway
		if statusErr, ok := err.(coreexecutor.StatusError); ok && statusErr.StatusCode() > 0 {
			status = statusErr.StatusCode()
		}
		chunk := handlers.BuildOpenAIResponsesStreamErrorChunk(status, err.Error(), 2)
		_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", string(chunk))
		flusher.Flush()
		return
	}
	text, err := openAIResponsesImageMarkdown(payload)
	if err != nil {
		chunk := handlers.BuildOpenAIResponsesStreamErrorChunk(http.StatusBadGateway, err.Error(), 2)
		_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", string(chunk))
		flusher.Flush()
		return
	}
	openAIResponsesWriteImageTextStream(c, flusher, responseID, createdAt, text)
}

func (h *OpenAIResponsesAPIHandler) writeImageResponseExecutionError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	if statusErr, ok := err.(coreexecutor.StatusError); ok && statusErr.StatusCode() > 0 {
		status = statusErr.StatusCode()
	}
	h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: status, Error: err})
}

func openAIResponsesImageMarkdown(payload []byte) (string, error) {
	data := gjson.GetBytes(payload, "data")
	if !data.IsArray() {
		return "", fmt.Errorf("image response returned no data array")
	}
	images := make([]string, 0)
	data.ForEach(func(_, item gjson.Result) bool {
		if b64 := strings.TrimSpace(item.Get("b64_json").String()); b64 != "" {
			images = append(images, fmt.Sprintf("![Generated image](data:image/png;base64,%s)", b64))
			return true
		}
		if url := strings.TrimSpace(item.Get("url").String()); url != "" {
			images = append(images, fmt.Sprintf("![Generated image](%s)", url))
		}
		return true
	})
	if len(images) == 0 {
		return "", fmt.Errorf("image response returned no images")
	}
	return strings.Join(images, "\n\n"), nil
}

func openAIResponsesImageResponseID() string {
	return fmt.Sprintf("resp_img_%d", time.Now().UnixNano())
}

func openAIResponsesImageJSON(text string) []byte {
	responseID := openAIResponsesImageResponseID()
	createdAt := time.Now().Unix()
	body := map[string]any{
		"id":         responseID,
		"object":     "response",
		"created_at": createdAt,
		"model":      openAIImageModelID,
		"status":     "completed",
		"output": []any{map[string]any{
			"id":     "msg_" + responseID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
				"logprobs":    []any{},
			}},
		}},
		"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
	}
	out, _ := json.Marshal(body)
	return out
}

func openAIResponsesImageEvent(eventType string, sequence int, responseID string, createdAt int64, text string, completed bool) []byte {
	status := "in_progress"
	output := []any{}
	if completed {
		status = "completed"
		output = []any{map[string]any{
			"id":      "msg_" + responseID,
			"type":    "message",
			"status":  "completed",
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}},
		}}
	}
	body := map[string]any{
		"type":            eventType,
		"sequence_number": sequence,
		"response": map[string]any{
			"id":         responseID,
			"object":     "response",
			"created_at": createdAt,
			"model":      openAIImageModelID,
			"status":     status,
			"background": false,
			"error":      nil,
			"output":     output,
		},
	}
	if completed {
		body["response"].(map[string]any)["usage"] = map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
			"total_tokens":  0,
		}
	}
	out, _ := json.Marshal(body)
	return out
}

func openAIResponsesWriteImageTextStream(c *gin.Context, flusher http.Flusher, responseID string, createdAt int64, text string) {
	itemID := "msg_" + responseID
	openAIResponsesWriteSSE(c, flusher, "response.output_item.added", mustMarshalJSON(map[string]any{"type": "response.output_item.added", "sequence_number": 2, "output_index": 0, "item": map[string]any{"id": itemID, "type": "message", "status": "in_progress", "content": []any{}, "role": "assistant"}}))
	openAIResponsesWriteSSE(c, flusher, "response.content_part.added", mustMarshalJSON(map[string]any{"type": "response.content_part.added", "sequence_number": 3, "item_id": itemID, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": ""}}))
	openAIResponsesWriteSSE(c, flusher, "response.output_text.delta", mustMarshalJSON(map[string]any{"type": "response.output_text.delta", "sequence_number": 4, "item_id": itemID, "output_index": 0, "content_index": 0, "delta": text, "logprobs": []any{}}))
	openAIResponsesWriteSSE(c, flusher, "response.output_text.done", mustMarshalJSON(map[string]any{"type": "response.output_text.done", "sequence_number": 5, "item_id": itemID, "output_index": 0, "content_index": 0, "text": text, "logprobs": []any{}}))
	openAIResponsesWriteSSE(c, flusher, "response.content_part.done", mustMarshalJSON(map[string]any{"type": "response.content_part.done", "sequence_number": 6, "item_id": itemID, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": text}}))
	openAIResponsesWriteSSE(c, flusher, "response.output_item.done", mustMarshalJSON(map[string]any{"type": "response.output_item.done", "sequence_number": 7, "output_index": 0, "item": map[string]any{"id": itemID, "type": "message", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}}, "role": "assistant"}}))
	openAIResponsesWriteSSE(c, flusher, "response.completed", openAIResponsesImageEvent("response.completed", 8, responseID, createdAt, text, true))
	_, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

func openAIResponsesWriteSSE(c *gin.Context, flusher http.Flusher, event string, data []byte) {
	_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, string(data))
	flusher.Flush()
}

func mustMarshalJSON(value any) []byte {
	out, _ := json.Marshal(value)
	return out
}

func (h *OpenAIResponsesAPIHandler) Compact(c *gin.Context) {
	rawJSON, ok := handlers.ReadJSONRequestBody(c)
	if !ok {
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
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
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
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
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
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")

	setSSEHeaders := func() {
		handlers.PrepareStreamingResponse(c)
	}

	// Peek at the first chunk
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
			// Upstream failed immediately. Return proper error status and JSON.
			h.WriteErrorResponse(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// Stream closed without data? Send headers and done.
				setSSEHeaders()
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// Success! Set headers.
			setSSEHeaders()
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

			// Write first chunk logic (matching forwardResponsesStream)
			if bytes.HasPrefix(chunk, []byte("event:")) {
				_, _ = c.Writer.Write([]byte("\n"))
			}
			_, _ = c.Writer.Write(chunk)
			_, _ = c.Writer.Write([]byte("\n"))
			flusher.Flush()

			// Continue
			h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan)
			return
		}
	}
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			if bytes.HasPrefix(chunk, []byte("event:")) {
				_, _ = c.Writer.Write([]byte("\n"))
			}
			_, _ = c.Writer.Write(chunk)
			_, _ = c.Writer.Write([]byte("\n"))
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			if errMsg == nil {
				return
			}
			status := http.StatusInternalServerError
			if errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			chunk := handlers.BuildOpenAIResponsesStreamErrorChunk(status, errText, 0)
			_, _ = fmt.Fprintf(c.Writer, "\nevent: error\ndata: %s\n\n", string(chunk))
		},
		WriteDone: func() {
			_, _ = c.Writer.Write([]byte("\n"))
		},
	})
}
