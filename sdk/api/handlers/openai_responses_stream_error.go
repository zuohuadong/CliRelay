package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type openAIResponsesStreamErrorChunk struct {
	Type           string `json:"type"`
	Code           string `json:"code"`
	Message        string `json:"message"`
	SequenceNumber int    `json:"sequence_number"`
	Status         int    `json:"status"`
	Error          struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	Headers map[string]string `json:"headers"`
}

type openAIResponsesParsedStreamError struct {
	status  int
	code    string
	errType string
	message string
}

func openAIResponsesStreamErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "invalid_api_key"
	case http.StatusForbidden:
		return "insufficient_quota"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case http.StatusNotFound:
		return "model_not_found"
	case http.StatusRequestTimeout:
		return "request_timeout"
	default:
		if status >= http.StatusInternalServerError {
			return "internal_server_error"
		}
		if status >= http.StatusBadRequest {
			return "invalid_request_error"
		}
		return "unknown_error"
	}
}

func normalizeOpenAIResponsesStreamErrorCode(status int, code string, message string) string {
	code = strings.TrimSpace(code)
	lowerCode := strings.ToLower(code)
	lowerMessage := strings.ToLower(strings.TrimSpace(message))

	switch {
	case status == http.StatusRequestEntityTooLarge ||
		lowerCode == "context_length_exceeded" ||
		lowerCode == "context_too_large" ||
		strings.Contains(lowerMessage, "context length") ||
		strings.Contains(lowerMessage, "context_length") ||
		strings.Contains(lowerMessage, "maximum context") ||
		strings.Contains(lowerMessage, "too many tokens"):
		return "context_too_large"
	case lowerCode == "auth_unavailable" ||
		lowerCode == "auth_not_found" ||
		strings.Contains(lowerMessage, "auth_unavailable") ||
		strings.Contains(lowerMessage, "auth_not_found"):
		return "auth_unavailable"
	case strings.Contains(lowerMessage, "invalid signature in thinking block"):
		return "thinking_signature_invalid"
	case strings.Contains(lowerCode, "previous_response_not_found") ||
		(strings.Contains(lowerMessage, "previous_response_id") && strings.Contains(lowerMessage, "not found")):
		return "previous_response_not_found"
	case strings.Contains(lowerCode, "invalid_id_prefix") ||
		(strings.Contains(lowerMessage, "expected an id that begins with 'resp'")):
		return "invalid_id_prefix"
	default:
		if code != "" {
			return code
		}
		return openAIResponsesStreamErrorCode(status)
	}
}

func NormalizeOpenAIResponsesStreamErrorStatus(status int, code string, message string) int {
	if status <= 0 {
		return http.StatusInternalServerError
	}
	if normalizeOpenAIResponsesStreamErrorCode(status, code, message) == "context_too_large" && status == http.StatusBadRequest {
		return http.StatusRequestEntityTooLarge
	}
	return status
}

func openAIResponsesStreamErrorType(status int, code string) string {
	switch {
	case status == http.StatusUnauthorized || code == "invalid_api_key" || code == "auth_unavailable":
		return "authentication_error"
	case status == http.StatusTooManyRequests || code == "rate_limit_exceeded":
		return "rate_limit_error"
	case status >= http.StatusInternalServerError:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}

func parseOpenAIResponsesStreamError(status int, errText string) openAIResponsesParsedStreamError {
	if status <= 0 {
		status = http.StatusInternalServerError
	}

	message := strings.TrimSpace(errText)
	if message == "" {
		message = http.StatusText(status)
	}

	code := openAIResponsesStreamErrorCode(status)
	errType := openAIResponsesStreamErrorType(status, code)

	trimmed := strings.TrimSpace(errText)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		var payload map[string]any
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			if t, ok := payload["type"].(string); ok && strings.TrimSpace(t) == "error" {
				if m, ok := payload["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				}
				if v, ok := payload["code"]; ok && v != nil {
					if c, ok := v.(string); ok && strings.TrimSpace(c) != "" {
						code = strings.TrimSpace(c)
					} else {
						code = strings.TrimSpace(fmt.Sprint(v))
					}
				}
				if v, ok := payload["status"].(float64); ok && v > 0 {
					status = int(v)
				}
			}
			if e, ok := payload["error"].(map[string]any); ok {
				if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				}
				if v, ok := e["code"]; ok && v != nil {
					if c, ok := v.(string); ok && strings.TrimSpace(c) != "" {
						code = strings.TrimSpace(c)
					} else {
						code = strings.TrimSpace(fmt.Sprint(v))
					}
				}
				if v, ok := e["type"]; ok && v != nil {
					if t, ok := v.(string); ok && strings.TrimSpace(t) != "" {
						errType = strings.TrimSpace(t)
					} else {
						errType = strings.TrimSpace(fmt.Sprint(v))
					}
				}
			}
		}
	}

	if strings.TrimSpace(code) == "" {
		code = "unknown_error"
	}
	code = normalizeOpenAIResponsesStreamErrorCode(status, code, message)
	status = NormalizeOpenAIResponsesStreamErrorStatus(status, code, message)
	if strings.TrimSpace(errType) == "" || code == "context_too_large" || code == "auth_unavailable" {
		errType = openAIResponsesStreamErrorType(status, code)
	}

	return openAIResponsesParsedStreamError{
		status:  status,
		code:    code,
		errType: errType,
		message: message,
	}
}

func IsOpenAIResponsesContextWindowError(status int, errText string) bool {
	return parseOpenAIResponsesStreamError(status, errText).code == "context_too_large"
}

// BuildOpenAIResponsesResponseFailedChunk builds an official Responses API
// response.failed event payload. Current Codex CLI only treats a nested
// response.error.code of context_length_exceeded as a context-window failure.
func BuildOpenAIResponsesResponseFailedChunk(status int, errText string, sequenceNumber int) []byte {
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}
	parsed := parseOpenAIResponsesStreamError(status, errText)
	code := parsed.code
	if code == "context_too_large" {
		code = "context_length_exceeded"
	}
	if strings.TrimSpace(code) == "" {
		code = "unknown_error"
	}
	errType := parsed.errType
	if strings.TrimSpace(errType) == "" {
		errType = openAIResponsesStreamErrorType(parsed.status, code)
	}
	message := strings.TrimSpace(parsed.message)
	if message == "" {
		message = http.StatusText(parsed.status)
	}

	payload := map[string]any{
		"type":            "response.failed",
		"sequence_number": sequenceNumber,
		"response": map[string]any{
			"id":         fmt.Sprintf("resp_failed_%d", time.Now().UnixNano()),
			"object":     "response",
			"created_at": time.Now().Unix(),
			"status":     "failed",
			"background": false,
			"error": map[string]any{
				"code":    code,
				"type":    errType,
				"message": message,
			},
			"usage":    nil,
			"user":     nil,
			"metadata": map[string]any{},
		},
	}
	data, err := json.Marshal(payload)
	if err == nil && len(data) > 0 {
		return data
	}
	return []byte(`{"type":"response.failed","sequence_number":0,"response":{"id":"resp_failed","object":"response","status":"failed","background":false,"error":{"code":"context_length_exceeded","type":"invalid_request_error","message":"context window exceeded"},"usage":null,"user":null,"metadata":{}}}`)
}

// BuildOpenAIResponsesStreamErrorChunk builds an OpenAI Responses streaming error chunk.
//
// Important: OpenAI's HTTP error bodies are shaped like {"error":{...}}; those are valid for
// non-streaming responses, but streaming clients validate SSE `data:` payloads against a union
// of chunks that requires a top-level `type` field. Use response.failed with
// context_length_exceeded for Codex context-window compaction.
func BuildOpenAIResponsesStreamErrorChunk(status int, errText string, sequenceNumber int) []byte {
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}

	parsed := parseOpenAIResponsesStreamError(status, errText)

	chunk := openAIResponsesStreamErrorChunk{
		Type:           "error",
		Code:           parsed.code,
		Message:        parsed.message,
		SequenceNumber: sequenceNumber,
		Status:         parsed.status,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}
	chunk.Error.Code = parsed.code
	chunk.Error.Type = parsed.errType
	chunk.Error.Message = parsed.message

	data, err := json.Marshal(chunk)
	if err == nil {
		return data
	}

	// Extremely defensive fallback.
	fallback := openAIResponsesStreamErrorChunk{
		Type:           "error",
		Code:           "internal_server_error",
		Message:        parsed.message,
		SequenceNumber: sequenceNumber,
		Status:         http.StatusInternalServerError,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}
	fallback.Error.Code = "internal_server_error"
	fallback.Error.Type = "server_error"
	fallback.Error.Message = parsed.message
	data, _ = json.Marshal(fallback)
	if len(data) > 0 {
		return data
	}
	return []byte(`{"type":"error","code":"internal_server_error","message":"internal error","sequence_number":0,"status":500,"error":{"code":"internal_server_error","type":"server_error","message":"internal error"},"headers":{"Content-Type":"application/json"}}`)
}
