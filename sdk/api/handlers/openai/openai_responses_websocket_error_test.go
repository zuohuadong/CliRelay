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

func TestBuildResponsesWebsocketErrorPayloadWritesFailedForContextWindow(t *testing.T) {
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
		errorPayload, errWrite := writeResponsesWebsocketError(conn, nil, errMsg)
		if errWrite != nil {
			t.Fatalf("write websocket error: %v", errWrite)
		}
		if len(errorPayload) == 0 {
			t.Fatalf("expected websocket error payload")
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

	if _, failedPayload, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read websocket failed payload: %v", errRead)
	} else if got := gjson.GetBytes(failedPayload, "type").String(); got != "response.failed" {
		t.Fatalf("payload type = %q, want response.failed; payload=%s", got, failedPayload)
	} else if got := gjson.GetBytes(failedPayload, "response.status").String(); got != "failed" {
		t.Fatalf("response.status = %q, want failed; payload=%s", got, failedPayload)
	} else if got := gjson.GetBytes(failedPayload, "response.error.code").String(); got != "context_length_exceeded" {
		t.Fatalf("response.error.code = %q, want context_length_exceeded; payload=%s", got, failedPayload)
	}
}

func TestWriteResponsesWebsocketErrorWritesFailedForOverload(t *testing.T) {
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
			StatusCode: http.StatusServiceUnavailable,
			Error:      jsonError(`{"error":{"code":"server_is_overloaded","type":"service_unavailable_error","message":"Our servers are currently overloaded. Please try again later."}}`),
		}
		errorPayload, errWrite := writeResponsesWebsocketError(conn, nil, errMsg)
		if errWrite != nil {
			t.Fatalf("write websocket error: %v", errWrite)
		}
		if len(errorPayload) == 0 {
			t.Fatalf("expected websocket error payload")
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

	if _, failedPayload, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read websocket failed payload: %v", errRead)
	} else if got := gjson.GetBytes(failedPayload, "type").String(); got != "response.failed" {
		t.Fatalf("payload type = %q, want response.failed; payload=%s", got, failedPayload)
	} else if got := gjson.GetBytes(failedPayload, "response.status").String(); got != "failed" {
		t.Fatalf("response.status = %q, want failed; payload=%s", got, failedPayload)
	} else if got := gjson.GetBytes(failedPayload, "response.error.code").String(); got != "server_is_overloaded" {
		t.Fatalf("response.error.code = %q, want server_is_overloaded; payload=%s", got, failedPayload)
	}
}

func TestWriteResponsesWebsocketErrorSynthesizesCompletedForRateLimit(t *testing.T) {
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
		errorPayload, errWrite := writeResponsesWebsocketError(conn, nil, errMsg)
		if errWrite != nil {
			t.Fatalf("write websocket error: %v", errWrite)
		}
		if len(errorPayload) == 0 {
			t.Fatalf("expected websocket error payload")
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

	if _, itemPayload, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read websocket output item payload: %v", errRead)
	} else if got := gjson.GetBytes(itemPayload, "type").String(); got != "response.output_item.done" {
		t.Fatalf("first payload type = %q, want response.output_item.done; payload=%s", got, itemPayload)
	} else if !strings.Contains(gjson.GetBytes(itemPayload, "item.content.0.text").String(), "rate_limit_exceeded") {
		t.Fatalf("output item must mention rate_limit_exceeded; payload=%s", itemPayload)
	}
	if _, completedPayload, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read websocket completed payload: %v", errRead)
	} else if got := gjson.GetBytes(completedPayload, "type").String(); got != "response.completed" {
		t.Fatalf("second payload type = %q, want response.completed; payload=%s", got, completedPayload)
	} else if got := gjson.GetBytes(completedPayload, "response.status").String(); got != "completed" {
		t.Fatalf("response.status = %q, want completed; payload=%s", got, completedPayload)
	} else if got := gjson.GetBytes(completedPayload, "response.id").String(); got != "" {
		t.Fatalf("synthetic error completion must not provide reusable response id, got %q; payload=%s", got, completedPayload)
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
