package openai

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/tidwall/gjson"
)

func TestBuildResponsesWebsocketErrorPayloadIncludesNestedErrorForCodexClients(t *testing.T) {
	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusRequestEntityTooLarge,
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
