package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Codex classifies a Responses error by code: server_is_overloaded and
// slow_down are fatal (it prints "Selected model is at capacity. Please try a
// different model." and ends the session), while server_error is retried with
// backoff. A forwarded capacity refusal therefore has to lose its code, not its
// message.
func TestRewriteCapacityShedCodeOnForwardedFailure(t *testing.T) {
	payload := []byte(`{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"type":"service_unavailable_error","code":"server_is_overloaded","headers":{"retry-after":"326"},"message":"Our servers are currently overloaded. Please try again later."}}}`)

	got, changed := RewriteCapacityShedCode(payload)
	if !changed {
		t.Fatalf("capacity failure was not rewritten: %s", payload)
	}
	if code := gjson.GetBytes(got, "response.error.code").String(); code != "server_error" {
		t.Fatalf("code = %q, want server_error", code)
	}
	if msg := gjson.GetBytes(got, "response.error.message").String(); !strings.Contains(msg, "currently overloaded") {
		t.Fatalf("message was lost: %q", msg)
	}
	if gjson.GetBytes(got, "response.status").String() != "failed" || gjson.GetBytes(got, "type").String() != "response.failed" {
		t.Fatalf("event shape changed: %s", got)
	}
}

func TestRewriteCapacityShedCodeHandlesSlowDownAndMessages(t *testing.T) {
	cases := []string{
		`{"type":"error","error":{"code":"slow_down","message":"slow down"}}`,
		`{"type":"error","error":{"message":"Selected model is at capacity. Please try a different model."}}`,
		`{"error":{"type":"service_unavailable_error","message":"Our servers are currently overloaded."}}`,
	}
	for _, payload := range cases {
		got, changed := RewriteCapacityShedCode([]byte(payload))
		if !changed {
			t.Fatalf("not rewritten: %s", payload)
		}
		code := gjson.GetBytes(got, "error.code").String()
		if code != "server_error" {
			t.Fatalf("code = %q, want server_error; payload=%s", code, got)
		}
	}
}

// A throttling code carries the client's own retry delay; rewriting it would
// make the client back off on the wrong schedule.
func TestRewriteCapacityShedCodeLeavesRetryableCodesAlone(t *testing.T) {
	for _, payload := range []string{
		`{"type":"error","error":{"code":"rate_limit_exceeded","message":"rate limit"}}`,
		`{"type":"error","error":{"code":"insufficient_quota","message":"quota"}}`,
		`{"type":"error","error":{"code":"invalid_request_error","message":"bad input"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	} {
		if got, changed := RewriteCapacityShedCode([]byte(payload)); changed {
			t.Fatalf("rewrote %s into %s", payload, got)
		}
	}
}

func TestSanitizeCapacityShedStreamChunkLeavesFramingAlone(t *testing.T) {
	chunk := []byte(`data: {"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded."}}}`)
	got := string(SanitizeCapacityShedStreamChunk(chunk))
	if !strings.HasPrefix(got, "data: ") || strings.Contains(got, "server_is_overloaded") {
		t.Fatalf("chunk = %q", got)
	}

	partial := []byte(`data: {"type":"response.failed","response":{"error":{"code":"server_is_ov`)
	if got := string(SanitizeCapacityShedStreamChunk(partial)); got != string(partial) {
		t.Fatalf("partial chunk was rewritten: %q", got)
	}
	multiline := []byte("event: error\ndata: {\"error\":{\"code\":\"server_is_overloaded\"}}")
	if got := string(SanitizeCapacityShedStreamChunk(multiline)); got != string(multiline) {
		t.Fatalf("multi-line chunk was rewritten: %q", got)
	}
}

// A non-streaming request fails with a plain HTTP body; the same code rule has
// to apply there.
func TestWriteErrorResponseRewritesCapacityShedCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	NewBaseAPIHandlers(nil, nil).WriteErrorResponseAs(c, &StreamErrorMessage{
		StatusCode: http.StatusServiceUnavailable,
		Error:      errString(`{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`),
	}, ErrorFormatOpenAI)

	body := recorder.Body.String()
	if strings.Contains(body, "server_is_overloaded") {
		t.Fatalf("capacity code reached the client: %s", body)
	}
	if !strings.Contains(body, "server_error") || !strings.Contains(body, "currently overloaded") {
		t.Fatalf("body = %s", body)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
