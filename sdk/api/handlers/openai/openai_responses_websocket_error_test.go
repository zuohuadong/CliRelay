package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
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

func TestBuildResponsesWebsocketTerminalFailedPayloadUsesCodexContextCode(t *testing.T) {
	errorPayload, err := buildResponsesWebsocketErrorPayload(&interfaces.ErrorMessage{
		StatusCode: http.StatusRequestEntityTooLarge,
		Error:      jsonError(`{"error":{"message":"request policy glm-5.1-large-request-guard blocked upstream model glm-5.1 via provider bigmodel-coding: request_bytes 600749 exceeds max-request-bytes 600000","type":"invalid_request_error","code":"context_length_exceeded"}}`),
	})
	if err != nil {
		t.Fatalf("buildResponsesWebsocketErrorPayload error: %v", err)
	}

	payload, err := buildResponsesWebsocketTerminalFailedPayload(errorPayload)
	if err != nil {
		t.Fatalf("buildResponsesWebsocketTerminalFailedPayload error: %v", err)
	}

	if got := gjson.GetBytes(payload, "type").String(); got != "response.failed" {
		t.Fatalf("type = %q, want response.failed; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.status").String(); got != "failed" {
		t.Fatalf("response.status = %q, want failed; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.error.code").String(); got != "context_length_exceeded" {
		t.Fatalf("response.error.code = %q, want context_length_exceeded; payload=%s", got, payload)
	}
	if got := int(gjson.GetBytes(payload, "status").Int()); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; payload=%s", got, http.StatusRequestEntityTooLarge, payload)
	}
}

func TestWriteResponsesWebsocketErrorWithTerminalCompletedSendsResponseFailedForContextTooLarge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() {
			_ = conn.Close()
		}()

		errMsg := &interfaces.ErrorMessage{
			StatusCode: http.StatusRequestEntityTooLarge,
			Error:      jsonError(`{"error":{"message":"request policy glm-5.1-large-request-guard blocked upstream model glm-5.1 via provider bigmodel-coding: request_bytes 600749 exceeds max-request-bytes 600000","type":"invalid_request_error","code":"context_length_exceeded"}}`),
		}
		errorPayload, completedPayload, errWrite := writeResponsesWebsocketErrorWithTerminalCompleted(conn, nil, errMsg, true)
		if errWrite != nil {
			t.Fatalf("write websocket error: %v", errWrite)
		}
		if len(errorPayload) == 0 || len(completedPayload) != 0 {
			t.Fatalf("expected response.failed payload only, got error=%d completed=%d", len(errorPayload), len(completedPayload))
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	if _, payload, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read websocket failed payload: %v", errRead)
	} else if got := gjson.GetBytes(payload, "type").String(); got != "response.failed" {
		t.Fatalf("payload type = %q, want response.failed; payload=%s", got, payload)
	} else if got := gjson.GetBytes(payload, "response.error.code").String(); got != "context_length_exceeded" {
		t.Fatalf("response.error.code = %q, want context_length_exceeded; payload=%s", got, payload)
	}
}

func TestWriteResponsesWebsocketErrorWithTerminalCompletedSendsTopLevelErrorForRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() {
			_ = conn.Close()
		}()

		errMsg := &interfaces.ErrorMessage{
			StatusCode: http.StatusTooManyRequests,
			Error:      jsonError(`{"error":{"message":"usage limit reached","type":"rate_limit_error","code":"rate_limit_exceeded"}}`),
		}
		errorPayload, completedPayload, errWrite := writeResponsesWebsocketErrorWithTerminalCompleted(conn, nil, errMsg, true)
		if errWrite != nil {
			t.Fatalf("write websocket error: %v", errWrite)
		}
		if len(errorPayload) == 0 || len(completedPayload) != 0 {
			t.Fatalf("expected top-level error payload only, got error=%d completed=%d", len(errorPayload), len(completedPayload))
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	if _, payload, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read websocket error payload: %v", errRead)
	} else if got := gjson.GetBytes(payload, "type").String(); got != "error" {
		t.Fatalf("payload type = %q, want error; payload=%s", got, payload)
	} else if gjson.GetBytes(payload, "response.id").Exists() {
		t.Fatalf("top-level error must not create a synthetic response id; payload=%s", payload)
	} else if got := gjson.GetBytes(payload, "error.code").String(); got != "rate_limit_exceeded" {
		t.Fatalf("error.code = %q, want rate_limit_exceeded; payload=%s", got, payload)
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
