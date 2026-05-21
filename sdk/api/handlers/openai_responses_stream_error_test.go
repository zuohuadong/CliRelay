package handlers

import (
	"encoding/json"
	"net/http"
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
	if payload["code"] != "internal_server_error" {
		t.Fatalf("code = %v, want %q", payload["code"], "internal_server_error")
	}
	if payload["message"] != "unexpected EOF" {
		t.Fatalf("message = %v, want %q", payload["message"], "unexpected EOF")
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
	if payload["code"] != "internal_server_error" {
		t.Fatalf("code = %v, want %q", payload["code"], "internal_server_error")
	}
	if payload["message"] != "oops" {
		t.Fatalf("message = %v, want %q", payload["message"], "oops")
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing nested error object: %#v", payload["error"])
	}
	if errorPayload["type"] != "server_error" {
		t.Fatalf("error.type = %v, want %q", errorPayload["type"], "server_error")
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
