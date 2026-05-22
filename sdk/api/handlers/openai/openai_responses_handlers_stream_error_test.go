package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestForwardResponsesStreamTerminalErrorSynthesizesAssistantMessageAndCompleted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatalf("expected gin writer to implement http.Flusher")
	}

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: errors.New("unexpected EOF")}
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.output_item.done") {
		t.Fatalf("expected output_item.done event, got: %q", body)
	}
	if !strings.Contains(body, "event: response.completed") {
		t.Fatalf("expected response.completed event, got: %q", body)
	}
	if !strings.Contains(body, `"type":"response.completed"`) {
		t.Fatalf("expected response.completed payload, got: %q", body)
	}
	if !strings.Contains(body, `"type":"message"`) {
		t.Fatalf("expected assistant message payload, got: %q", body)
	}
	if !strings.Contains(body, `Upstream request failed`) {
		t.Fatalf("expected fallback error text, got: %q", body)
	}
}

func TestForwardResponsesStreamTerminalContextErrorWritesFailed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatalf("expected gin writer to implement http.Flusher")
	}

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusRequestEntityTooLarge,
		Error:      errors.New(`{"error":{"message":"request policy glm-5.1-large-request-guard blocked upstream model glm-5.1 via provider bigmodel-coding: request_bytes 706275 exceeds max-request-bytes 600000","type":"invalid_request_error","code":"context_length_exceeded"}}`),
	}
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("expected response.failed event, got: %q", body)
	}
	if !strings.Contains(body, `"type":"response.failed"`) {
		t.Fatalf("expected response.failed payload, got: %q", body)
	}
	if strings.Contains(body, "event: response.output_item.done") || strings.Contains(body, "event: response.completed") {
		t.Fatalf("context window terminal error must not synthesize completed output, got: %q", body)
	}
	if !strings.Contains(body, `context_length_exceeded`) {
		t.Fatalf("expected context_length_exceeded marker in failed payload, got: %q", body)
	}
}
