package executor

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestIsCodexTerminalEmptyIncomplete(t *testing.T) {
	const empty = `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","output":[],"usage":{"input_tokens":12,"output_tokens":0,"total_tokens":12}}}`
	if !isCodexTerminalEmptyIncomplete([]byte(empty), 0, false) {
		t.Fatal("an incomplete turn with zero output tokens and no output is an empty turn")
	}

	cases := []struct {
		name  string
		event string
		items int
		delta bool
	}{
		{"completed", `{"type":"response.completed","response":{"output":[],"usage":{"output_tokens":0}}}`, 0, false},
		{"tokens produced", `{"type":"response.incomplete","response":{"output":[],"usage":{"output_tokens":2}}}`, 0, false},
		{"fractional tokens", `{"type":"response.incomplete","response":{"output":[],"usage":{"output_tokens":0.5}}}`, 0, false},
		{"tokens missing", `{"type":"response.incomplete","response":{"output":[]}}`, 0, false},
		{"output items", `{"type":"response.incomplete","response":{"output":[],"usage":{"output_tokens":0}}}`, 1, false},
		{"output in the event", `{"type":"response.incomplete","response":{"output":[{"type":"message"}],"usage":{"output_tokens":0}}}`, 0, false},
		{"a delta was seen", `{"type":"response.incomplete","response":{"output":[],"usage":{"output_tokens":0}}}`, 0, true},
	}
	for _, tc := range cases {
		if isCodexTerminalEmptyIncomplete([]byte(tc.event), tc.items, tc.delta) {
			t.Fatalf("%s must not be read as an empty turn", tc.name)
		}
	}
}

// codexEmptyIncompleteStream ends a turn that produced nothing: no items, no
// delta, and a usage block that says so.
const codexEmptyIncompleteStream = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
	"event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"incomplete\",\"output\":[],\"incomplete_details\":{\"reason\":\"server_error\"},\"usage\":{\"input_tokens\":12,\"output_tokens\":0,\"total_tokens\":12}}}\n\n"

func TestCodexEmptyIncompleteFailsTheTurn(t *testing.T) {
	env := newCodexExecEnv(t, bootstrapBufferedConfig())
	env.Upstream.respond = func(n int, _ codexWireCall) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(codexEmptyIncompleteStream)),
		}
	}

	out, err := env.Do(t, codexCall{Auth: codexOAuthAuth(), From: "openai-response", Payload: `{"model":"gpt-5.4","stream":true,"input":[]}`, Stream: true})
	if err == nil || !strings.Contains(err.Error(), codexEmptyIncompleteStreamMessage) {
		t.Fatalf("stream error = %v, want the empty-turn failure", err)
	}
	if strings.Contains(string(out), "response.incomplete") {
		t.Fatalf("the empty terminal was forwarded as a finished turn: %q", out)
	}
}

func TestCodexEmptyIncompleteNonStreamFailsTheTurn(t *testing.T) {
	env := newCodexExecEnv(t, nil)
	env.Upstream.respond = func(n int, _ codexWireCall) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(codexEmptyIncompleteStream)),
		}
	}

	if _, err := env.Do(t, codexCall{Auth: codexOAuthAuth(), From: "openai-response", Payload: `{"model":"gpt-5.4","input":[]}`}); err == nil ||
		!strings.Contains(err.Error(), codexEmptyIncompleteStreamMessage) {
		t.Fatalf("error = %v, want the empty-turn failure", err)
	}
}

// A turn the client capped itself still produced tokens, so it is an answer.
func TestCodexIncompleteWithOutputIsForwarded(t *testing.T) {
	const stream = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"incomplete\",\"output\":[],\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":12,\"output_tokens\":7,\"total_tokens\":19}}}\n\n"
	env := newCodexExecEnv(t, nil)
	env.Upstream.respond = func(n int, _ codexWireCall) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(stream)),
		}
	}

	out, err := env.Do(t, codexCall{Auth: codexOAuthAuth(), From: "openai-response", Payload: `{"model":"gpt-5.4","stream":true,"input":[]}`, Stream: true})
	if err != nil {
		t.Fatalf("a capped turn must not fail: %v", err)
	}
	if !strings.Contains(string(out), "response.incomplete") {
		t.Fatalf("the capped turn was not forwarded: %q", out)
	}
}
