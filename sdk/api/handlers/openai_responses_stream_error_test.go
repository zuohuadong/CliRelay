package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBuildOpenAIResponsesStreamErrorChunk(t *testing.T) {
	chunk := BuildOpenAIResponsesStreamErrorChunk(http.StatusInternalServerError, "unexpected EOF", 0)
	var payload map[string]any
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["type"] != "error" {
		t.Fatalf("type = %v, want %q", payload["type"], "error")
	}
	errorObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error is not an object: %v", payload["error"])
	}
	if errorObj["code"] != "internal_server_error" {
		t.Fatalf("code = %v, want %q", errorObj["code"], "internal_server_error")
	}
	if errorObj["message"] != "unexpected EOF" {
		t.Fatalf("message = %v, want %q", errorObj["message"], "unexpected EOF")
	}
	if payload["sequence_number"] != float64(0) {
		t.Fatalf("sequence_number = %v, want %v", payload["sequence_number"], 0)
	}
	if payload["status"] != float64(http.StatusInternalServerError) {
		t.Fatalf("status = %v, want %v", payload["status"], http.StatusInternalServerError)
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing nested error object: %#v", payload["error"])
	}
	if errorPayload["code"] != "internal_server_error" {
		t.Fatalf("error.code = %v, want %q", errorPayload["code"], "internal_server_error")
	}
	if errorPayload["type"] != "server_error" {
		t.Fatalf("error.type = %v, want %q", errorPayload["type"], "server_error")
	}
	if errorPayload["message"] != "unexpected EOF" {
		t.Fatalf("error.message = %v, want %q", errorPayload["message"], "unexpected EOF")
	}
	headers, ok := payload["headers"].(map[string]any)
	if !ok || headers["Content-Type"] != "application/json" {
		t.Fatalf("headers = %#v, want Content-Type application/json", payload["headers"])
	}
}

func TestBuildOpenAIResponsesStreamErrorChunkExtractsHTTPErrorBody(t *testing.T) {
	chunk := BuildOpenAIResponsesStreamErrorChunk(
		http.StatusInternalServerError,
		`{"error":{"message":"oops","type":"server_error","code":"internal_server_error"}}`,
		0,
	)
	var payload map[string]any
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["type"] != "error" {
		t.Fatalf("type = %v, want %q", payload["type"], "error")
	}
	errorObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error is not an object: %v", payload["error"])
	}
	if errorObj["code"] != "internal_server_error" {
		t.Fatalf("code = %v, want %q", errorObj["code"], "internal_server_error")
	}
	if errorObj["message"] != "oops" {
		t.Fatalf("message = %v, want %q", errorObj["message"], "oops")
	}
	if errorObj["type"] != "server_error" {
		t.Fatalf("error.type = %v, want %q", errorObj["type"], "server_error")
	}
}

func TestBuildOpenAIResponsesStreamErrorChunkPreservesNestedError(t *testing.T) {
	errText := `{"error":{"type":"invalid_request","code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk.","param":null}}`
	chunk := BuildOpenAIResponsesStreamErrorChunk(http.StatusBadRequest, errText, 2)
	var payload struct {
		Type           string         `json:"type"`
		Error          map[string]any `json:"error"`
		SequenceNumber int            `json:"sequence_number"`
	}
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Type != "error" {
		t.Fatalf("type = %q, want %q", payload.Type, "error")
	}
	if payload.SequenceNumber != 2 {
		t.Fatalf("sequence_number = %d, want 2", payload.SequenceNumber)
	}
	if payload.Error["type"] != "invalid_request" {
		t.Fatalf("error.type = %v, want invalid_request", payload.Error["type"])
	}
	if payload.Error["code"] != "cyber_policy" {
		t.Fatalf("error.code = %v, want cyber_policy", payload.Error["code"])
	}
	if payload.Error["message"] != "This content was flagged for possible cybersecurity risk." {
		t.Fatalf("error.message = %v", payload.Error["message"])
	}
	if param, exists := payload.Error["param"]; !exists || param != nil {
		t.Fatalf("error.param = %v, want nil", param)
	}
}

func TestBuildOpenAIResponsesStreamErrorChunkPreservesCustomAndEmptyFields(t *testing.T) {
	// Preserves empty error object {}
	emptyChunk := BuildOpenAIResponsesStreamErrorChunk(http.StatusBadRequest, `{"error":{}}`, 0)
	var emptyPayload struct {
		Type           string         `json:"type"`
		Error          map[string]any `json:"error"`
		SequenceNumber int            `json:"sequence_number"`
	}
	if err := json.Unmarshal(emptyChunk, &emptyPayload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(emptyPayload.Error) != 0 {
		t.Fatalf("expected empty error object, got %v", emptyPayload.Error)
	}

	// Preserves custom fields and types without dropping
	customText := `{"error":{"type":"custom_type","code":"custom_code","custom_key":"custom_val","is_flag":true,"count":42}}`
	customChunk := BuildOpenAIResponsesStreamErrorChunk(http.StatusBadRequest, customText, 5)
	var customPayload struct {
		Type           string         `json:"type"`
		Error          map[string]any `json:"error"`
		SequenceNumber int            `json:"sequence_number"`
	}
	if err := json.Unmarshal(customChunk, &customPayload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if customPayload.SequenceNumber != 5 {
		t.Fatalf("sequence_number = %d, want 5", customPayload.SequenceNumber)
	}
	if customPayload.Error["custom_key"] != "custom_val" {
		t.Fatalf("custom_key = %v, want custom_val", customPayload.Error["custom_key"])
	}
	if customPayload.Error["is_flag"] != true {
		t.Fatalf("is_flag = %v, want true", customPayload.Error["is_flag"])
	}
	if customPayload.Error["count"] != float64(42) {
		t.Fatalf("count = %v, want 42", customPayload.Error["count"])
	}
}

func TestBuildOpenAIResponsesStreamErrorChunkPrioritizesPayloadSequenceNumber(t *testing.T) {
	errText := `{"error":{"type":"invalid_request","code":"blocked"},"sequence_number":7}`
	chunk := BuildOpenAIResponsesStreamErrorChunk(http.StatusBadRequest, errText, 2)
	var payload struct {
		Type           string         `json:"type"`
		Error          map[string]any `json:"error"`
		SequenceNumber int            `json:"sequence_number"`
	}
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.SequenceNumber != 7 {
		t.Fatalf("sequence_number = %d, want 7 (from payload)", payload.SequenceNumber)
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing nested error object: %#v", payload["error"])
	}
	if errorPayload["type"] != "server_error" {
		t.Fatalf("error.type = %v, want %q", errorPayload["type"], "server_error")
	}
}

func TestBuildOpenAIResponsesStreamErrorChunkPreservesPlainAuthUnavailable(t *testing.T) {
	chunk := BuildOpenAIResponsesStreamErrorChunk(
		http.StatusServiceUnavailable,
		"auth_unavailable: no auth available (providers=astron-code, model=deepseek-v4-pro)",
		0,
	)
	var payload map[string]any
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["code"] != "auth_unavailable" {
		t.Fatalf("code = %v, want %q", payload["code"], "auth_unavailable")
	}
	if payload["status"] != float64(http.StatusServiceUnavailable) {
		t.Fatalf("status = %v, want %v", payload["status"], http.StatusServiceUnavailable)
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing nested error object: %#v", payload["error"])
	}
	if errorPayload["type"] != "authentication_error" {
		t.Fatalf("error.type = %v, want %q", errorPayload["type"], "authentication_error")
	}
}

func TestBuildOpenAIResponsesStreamErrorChunkNormalizesContextTooLarge(t *testing.T) {
	chunk := BuildOpenAIResponsesStreamErrorChunk(
		http.StatusRequestEntityTooLarge,
		`{"error":{"message":"request policy glm-5.1-large-request-guard blocked upstream model glm-5.1 via provider bigmodel-coding: request_bytes 706275 exceeds max-request-bytes 600000","type":"invalid_request_error","code":"context_length_exceeded"}}`,
		0,
	)
	var payload map[string]any
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["type"] != "error" {
		t.Fatalf("type = %v, want %q", payload["type"], "error")
	}
	if payload["code"] != "context_too_large" {
		t.Fatalf("code = %v, want %q", payload["code"], "context_too_large")
	}
	if payload["status"] != float64(http.StatusRequestEntityTooLarge) {
		t.Fatalf("status = %v, want %v", payload["status"], http.StatusRequestEntityTooLarge)
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing nested error object: %#v", payload["error"])
	}
	if errorPayload["code"] != "context_too_large" {
		t.Fatalf("error.code = %v, want %q", errorPayload["code"], "context_too_large")
	}
	if errorPayload["type"] != "invalid_request_error" {
		t.Fatalf("error.type = %v, want %q", errorPayload["type"], "invalid_request_error")
	}
}

func TestBuildOpenAIResponsesResponseFailedChunkUsesCodexContextCode(t *testing.T) {
	chunk := BuildOpenAIResponsesResponseFailedChunk(
		http.StatusRequestEntityTooLarge,
		`{"error":{"message":"request policy glm-5.1-large-request-guard blocked upstream model glm-5.1 via provider bigmodel-coding: request_bytes 706275 exceeds max-request-bytes 600000","type":"invalid_request_error","code":"context_length_exceeded"}}`,
		0,
	)
	var payload map[string]any
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["type"] != "response.failed" {
		t.Fatalf("type = %v, want %q", payload["type"], "response.failed")
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		t.Fatalf("missing response object: %#v", payload["response"])
	}
	if response["status"] != "failed" {
		t.Fatalf("response.status = %v, want failed", response["status"])
	}
	errorPayload, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing response.error object: %#v", response["error"])
	}
	if errorPayload["code"] != "context_length_exceeded" {
		t.Fatalf("response.error.code = %v, want context_length_exceeded", errorPayload["code"])
	}
	if errorPayload["type"] != "invalid_request_error" {
		t.Fatalf("response.error.type = %v, want invalid_request_error", errorPayload["type"])
	}
}

func TestBuildOpenAIResponsesResponseFailedChunkClassifiesRequestScopedItemNotFound(t *testing.T) {
	chunk := BuildOpenAIResponsesResponseFailedChunk(http.StatusNotFound, requestScopedItemNotFoundErrorMessage, 0)
	var payload map[string]any
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		t.Fatalf("missing response object: %#v", payload["response"])
	}
	errorPayload, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing response.error object: %#v", response["error"])
	}
	if got := errorPayload["code"]; got != "item_not_found" {
		t.Fatalf("response.error.code = %v, want item_not_found", got)
	}
	if got := errorPayload["type"]; got != "invalid_request_error" {
		t.Fatalf("response.error.type = %v, want invalid_request_error", got)
	}
}

func TestBuildOpenAIResponsesResponseFailedChunkRecognizesCodexContextWindowText(t *testing.T) {
	errText := "Codex ran out of room in the model's context window. Start a new thread or clear earlier history before retrying."
	if !IsOpenAIResponsesContextWindowError(http.StatusInternalServerError, errText) {
		t.Fatalf("expected Codex context window text to be recognized")
	}
	chunk := BuildOpenAIResponsesResponseFailedChunk(http.StatusInternalServerError, errText, 0)
	var payload map[string]any
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		t.Fatalf("missing response object: %#v", payload["response"])
	}
	errorPayload, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing response.error object: %#v", response["error"])
	}
	if errorPayload["code"] != "context_length_exceeded" {
		t.Fatalf("response.error.code = %v, want context_length_exceeded", errorPayload["code"])
	}
	if errorPayload["type"] != "invalid_request_error" {
		t.Fatalf("response.error.type = %v, want invalid_request_error", errorPayload["type"])
	}
	if errorPayload["message"] != errText {
		t.Fatalf("response.error.message = %v, want %q", errorPayload["message"], errText)
	}
}

func TestBuildOpenAIResponsesStreamErrorChunkPreservesUpstreamUnavailableContextDiagnostics(t *testing.T) {
	errText := `{"error":{"message":"upstream_model_unavailable: no executable upstream model available (provider=astron-code, model=deepseek-v4-pro, request_bytes=121, candidates=xopdeepseekv4pro,astron-code-latest, candidate_context_lengths=astron-code-latest:500000)","type":"server_error","code":"internal_server_error"}}`
	if IsOpenAIResponsesContextWindowError(http.StatusServiceUnavailable, errText) {
		t.Fatalf("upstream model availability diagnostics must not be treated as context window overflow")
	}

	chunk := BuildOpenAIResponsesStreamErrorChunk(http.StatusServiceUnavailable, errText, 0)
	var payload map[string]any
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["code"] != "internal_server_error" {
		t.Fatalf("code = %v, want internal_server_error", payload["code"])
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing nested error object: %#v", payload["error"])
	}
	if errorPayload["type"] != "server_error" {
		t.Fatalf("error.type = %v, want server_error", errorPayload["type"])
	}
	if errorPayload["code"] != "internal_server_error" {
		t.Fatalf("error.code = %v, want internal_server_error", errorPayload["code"])
	}
}

func TestBuildOpenAIResponsesStreamFailedChunkPreservesNestedError(t *testing.T) {
	chunk := BuildOpenAIResponsesStreamFailedChunk(
		http.StatusBadRequest,
		`{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked","param":null}}`,
		0,
	)

	var payload struct {
		Type           string `json:"type"`
		SequenceNumber int    `json:"sequence_number"`
		Response       struct {
			Status string `json:"status"`
			Error  struct {
				Type    string `json:"type"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Type != "response.failed" {
		t.Fatalf("type = %q, want %q", payload.Type, "response.failed")
	}
	if payload.SequenceNumber != 0 {
		t.Fatalf("sequence_number = %d, want 0", payload.SequenceNumber)
	}
	if payload.Response.Status != "failed" {
		t.Fatalf("response.status = %q, want %q", payload.Response.Status, "failed")
	}
	if payload.Response.Error.Type != "invalid_request" {
		t.Fatalf("response.error.type = %q, want %q", payload.Response.Error.Type, "invalid_request")
	}
	if payload.Response.Error.Code != "cyber_policy" {
		t.Fatalf("response.error.code = %q, want %q", payload.Response.Error.Code, "cyber_policy")
	}
	if payload.Response.Error.Message != "blocked" {
		t.Fatalf("response.error.message = %q, want %q", payload.Response.Error.Message, "blocked")
	}
}

func TestBuildOpenAIResponsesStreamFailedChunkPrioritizesPayloadSequenceNumber(t *testing.T) {
	chunk := BuildOpenAIResponsesStreamFailedChunk(
		http.StatusBadRequest,
		`{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"},"sequence_number":7}`,
		2,
	)

	var payload struct {
		Type           string `json:"type"`
		SequenceNumber int    `json:"sequence_number"`
	}
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.SequenceNumber != 7 {
		t.Fatalf("sequence_number = %d, want 7 (from payload over arg 2)", payload.SequenceNumber)
	}
}

func TestBuildOpenAIResponsesStreamErrorChunkPreservesLargeIntPrecision(t *testing.T) {
	errText := `{"error":{"type":"invalid_request","code":"blocked","request_id":9007199254740993}}`
	chunk := BuildOpenAIResponsesStreamErrorChunk(http.StatusBadRequest, errText, 0)
	raw := string(chunk)
	if !strings.Contains(raw, "9007199254740993") {
		t.Fatalf("large integer precision was lost in error chunk: %s", raw)
	}
	if strings.Contains(raw, "9007199254740992") {
		t.Fatalf("large integer was corrupted by float64 in error chunk: %s", raw)
	}

	failedChunk := BuildOpenAIResponsesStreamFailedChunk(http.StatusBadRequest, errText, 0)
	failedRaw := string(failedChunk)
	if !strings.Contains(failedRaw, "9007199254740993") {
		t.Fatalf("large integer precision was lost in failed chunk: %s", failedRaw)
	}
	if strings.Contains(failedRaw, "9007199254740992") {
		t.Fatalf("large integer was corrupted by float64 in failed chunk: %s", failedRaw)
	}
}
