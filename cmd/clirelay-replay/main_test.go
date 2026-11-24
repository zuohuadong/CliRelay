package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseSSEReportsEventsAndTerminal(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created"}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1"}}`,
		"",
	}, "\n")

	var raw bytes.Buffer
	var events []string
	if msg := parseSSE(strings.NewReader(stream), &raw, func(e string) { events = append(events, e) }); msg != "" {
		t.Fatalf("error message = %q, want none", msg)
	}
	if strings.Join(events, ",") != "response.created,response.output_text.delta,response.completed" {
		t.Fatalf("events = %v", events)
	}
	if !bytes.Contains(raw.Bytes(), []byte("response.completed")) {
		t.Fatalf("raw stream not captured: %q", raw.String())
	}
}

// An error frame is what the client turns into "stream closed before
// response.completed"; the replay must surface its message, not just its name.
func TestParseSSESurfacesTheUpstreamError(t *testing.T) {
	stream := "event: error\n" +
		`data: {"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}` + "\n\n"

	var raw bytes.Buffer
	var events []string
	msg := parseSSE(strings.NewReader(stream), &raw, func(e string) { events = append(events, e) })
	if len(events) != 1 || events[0] != "error" {
		t.Fatalf("events = %v", events)
	}
	if !strings.Contains(msg, "currently overloaded") {
		t.Fatalf("error message = %q", msg)
	}
}

func TestParseSSEUsesThePayloadTypeWhenTheStreamDoesNotNameIt(t *testing.T) {
	stream := "data: {\"type\":\"response.completed\",\"response\":{}}\n\n"
	var raw bytes.Buffer
	var events []string
	parseSSE(strings.NewReader(stream), &raw, func(e string) { events = append(events, e) })
	if len(events) != 1 || events[0] != "response.completed" {
		t.Fatalf("events = %v", events)
	}
}

func TestParseSSEJoinsFragmentedData(t *testing.T) {
	stream := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":\ndata: {\"error\":{\"message\":\"boom\"}}}\n\n"
	var raw bytes.Buffer
	var events []string
	msg := parseSSE(strings.NewReader(stream), &raw, func(e string) { events = append(events, e) })
	if len(events) != 1 || events[0] != "response.failed" {
		t.Fatalf("events = %v", events)
	}
	if msg != "boom" {
		t.Fatalf("error message = %q, want boom", msg)
	}
}

func TestReplayOnceFlagsAMissingTerminalEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"))
	}))
	defer server.Close()

	result, err := replayOnce(options{base: server.URL, path: "/v1/responses", apiKey: "sk-test", timeout: 5 * time.Second}, []byte(`{"model":"m","stream":true}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.terminal {
		t.Fatalf("a stream without a terminal event was reported as terminal")
	}
	if len(result.events) != 1 || result.events[0] != "response.created" {
		t.Fatalf("events = %v", result.events)
	}
}

func TestReplayOnceReportsATerminalStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n"))
	}))
	defer server.Close()

	result, err := replayOnce(options{base: server.URL, path: "/v1/responses", apiKey: "sk-test", timeout: 5 * time.Second}, []byte(`{"model":"m","stream":true}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.terminal {
		t.Fatalf("terminal event was not recognised: %v", result.events)
	}
}

func TestUpstreamLogTailKeepsTheEndOfTheStream(t *testing.T) {
	detail := []byte(`{"response":{"upstream_log":"event: response.created\n\nevent: error\n\ndata: {\"type\":\"error\",\"error\":{\"code\":\"server_is_overloaded\"}}"}}`)
	tail := upstreamLogTail(detail, 1000)
	if !strings.Contains(tail, "server_is_overloaded") {
		t.Fatalf("tail = %q", tail)
	}
	if len(tail) > 1001 {
		t.Fatalf("tail not bounded: %d bytes", len(tail))
	}
}

func TestWithModelForcesStreaming(t *testing.T) {
	body, err := withModel([]byte(`{"model":"a","stream":false,"input":"hi"}`), "grok-4.7")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"stream":true`) || !strings.Contains(string(body), `"model":"grok-4.7"`) {
		t.Fatalf("body = %s", body)
	}
}
