package openai

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/tidwall/gjson"
)

func TestBuildResponsesWebsocketErrorPayloadIncludesNestedErrorForCodexClients(t *testing.T) {
	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      jsonError(`{"error":{"message":"request policy glm-5.1-large-request-guard blocked upstream model glm-5.1 via provider bigmodel-coding: request_bytes 706275 exceeds max-request-bytes 600000","type":"invalid_request_error","code":"context_length_exceeded"}}`),
	}

	payload, err := buildResponsesWebsocketErrorPayload(errMsg)
	if err != nil {
		t.Fatalf("buildResponsesWebsocketErrorPayload error: %v", err)
	}

	if got := gjson.GetBytes(payload, "type").String(); got != "error" {
		t.Fatalf("type = %q, want error; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "code").String(); got != "context_too_large" {
		t.Fatalf("code = %q, want context_too_large; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "error.code").String(); got != "context_too_large" {
		t.Fatalf("error.code = %q, want context_too_large; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "error.message").String(); got == "" {
		t.Fatalf("expected nested error.message, payload=%s", payload)
	}
	if got := int(gjson.GetBytes(payload, "status").Int()); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; payload=%s", got, http.StatusRequestEntityTooLarge, payload)
	}
}

func TestBuildResponsesWebsocketTerminalCompletedPayloadCarriesNestedError(t *testing.T) {
	errorPayload, err := buildResponsesWebsocketErrorPayload(&interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      jsonError(`{"error":{"message":"usage limit reached","type":"rate_limit_error","code":"rate_limit_exceeded"}}`),
	})
	if err != nil {
		t.Fatalf("buildResponsesWebsocketErrorPayload error: %v", err)
	}

	payload, err := buildResponsesWebsocketTerminalCompletedPayload(errorPayload)
	if err != nil {
		t.Fatalf("buildResponsesWebsocketTerminalCompletedPayload error: %v", err)
	}

	if got := gjson.GetBytes(payload, "type").String(); got != "response.completed" {
		t.Fatalf("type = %q, want response.completed; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.status").String(); got != "completed" {
		t.Fatalf("response.status = %q, want completed; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.error.code").String(); got != "rate_limit_exceeded" {
		t.Fatalf("response.error.code = %q, want rate_limit_exceeded; payload=%s", got, payload)
	}
	if got := int(gjson.GetBytes(payload, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; payload=%s", got, http.StatusTooManyRequests, payload)
	}
	if got := gjson.GetBytes(payload, "response.error.type").String(); got != "invalid_request_error" {
		t.Fatalf("response.error.type = %q, want invalid_request_error; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.id").String(); got == "" {
		t.Fatalf("expected response.id, payload=%s", payload)
	}
}

func jsonError(raw string) error {
	return &websocketErrorString{raw: raw}
}

type websocketErrorString struct {
	raw string
}

func (e *websocketErrorString) Error() string {
	if e == nil {
		return ""
	}
	return e.raw
}
