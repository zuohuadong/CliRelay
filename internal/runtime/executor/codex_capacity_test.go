package executor

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

const codexCapacityBody = `{"error":{"message":"Selected model is at capacity. Please try a different model."}}`

func TestCodexCapacityErrorsAreRetryableRateLimits(t *testing.T) {
	err := newCodexStatusErr(http.StatusBadRequest, []byte(codexCapacityBody))
	if got := err.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}

	terminal := codexResponsesFailedStatusErr([]byte(`{"type":"error","error":{"message":"Selected model is at capacity. Please try a different model."}}`))
	if got := terminal.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("stream status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if !isCodexCapacityStatusErr(terminal) {
		t.Fatalf("a capacity rejection must be recognisable as one")
	}

	// A generic rate limit keeps its existing handling.
	generic := codexResponsesFailedStatusErr([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit reached for requests"}}`))
	if isCodexCapacityStatusErr(generic) {
		t.Fatalf("a generic rate limit must not be treated as capacity")
	}
}

// codexCapacityStream is what the upstream sends when the selected model is
// full: the handshake first, the rejection as a later frame.
const codexCapacityStream = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
	"event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"Selected model is at capacity. Please try a different model.\"}}\n\n"

func TestCodexBootstrapBufferingKeepsCapacityRejectionRetryable(t *testing.T) {
	env := newCodexExecEnv(t, bootstrapBufferedConfig())
	env.Upstream.respond = func(n int, _ codexWireCall) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(codexCapacityStream)),
		}
	}

	out, err := env.Do(t, codexCall{Auth: codexOAuthAuth(), From: "openai-response", Payload: `{"model":"gpt-5.4","stream":true,"input":[]}`, Stream: true})
	if len(out) != 0 {
		t.Fatalf("handshake reached the client before the rejection, so the attempt is committed: %q", out)
	}
	if err == nil || !isCodexCapacityStatusErr(err) {
		t.Fatalf("stream error = %v, want a retryable capacity rejection", err)
	}
}

func TestCodexBootstrapBufferingFlushesOnRealOutput(t *testing.T) {
	const stream = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"
	env := newCodexExecEnv(t, bootstrapBufferedConfig())
	env.Upstream.respond = func(n int, _ codexWireCall) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(stream)),
		}
	}

	out, err := env.Do(t, codexCall{Auth: codexOAuthAuth(), From: "openai-response", Payload: `{"model":"gpt-5.4","stream":true,"input":[]}`, Stream: true})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if !strings.Contains(string(out), "response.created") {
		t.Fatalf("the held handshake was never flushed: %q", out)
	}
	if !strings.Contains(string(out), `"hi"`) {
		t.Fatalf("output delta missing: %q", out)
	}
}

// bootstrapBufferedConfig turns the opt-in buffering on for a test.
func bootstrapBufferedConfig() *config.Config {
	on := true
	return &config.Config{SDKConfig: config.SDKConfig{Codex: config.CodexExecutorOptions{StreamBootstrapBuffering: &on}}}
}

func TestCodexBootstrapBufferingIsOptIn(t *testing.T) {
	env := newCodexExecEnv(t, nil)
	env.Upstream.respond = func(n int, _ codexWireCall) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(codexCapacityStream)),
		}
	}

	out, err := env.Do(t, codexCall{Auth: codexOAuthAuth(), From: "openai-response", Payload: `{"model":"gpt-5.4","stream":true,"input":[]}`, Stream: true})
	if err == nil {
		t.Fatalf("expected the capacity rejection")
	}
	if !strings.Contains(string(out), "response.created") {
		t.Fatalf("with buffering off (the default) the handshake must be forwarded at once: %q", out)
	}
}
