package executor

import "testing"

func TestXAIIsKeepaliveEventType(t *testing.T) {
	t.Parallel()
	for _, eventType := range []string{"keepalive", "response.keepalive", "ping", "heartbeat"} {
		if !xaiIsKeepaliveEventType(eventType) {
			t.Fatalf("xaiIsKeepaliveEventType(%q) = false, want true", eventType)
		}
	}
	if xaiIsKeepaliveEventType("response.completed") {
		t.Fatal("completed should not be a keepalive event")
	}
}

func TestXAIIsTerminalResponseEventType(t *testing.T) {
	t.Parallel()
	for _, eventType := range []string{"response.completed", "response.done", "response.incomplete", "response.failed", "error"} {
		if !xaiIsTerminalResponseEventType(eventType) {
			t.Fatalf("xaiIsTerminalResponseEventType(%q) = false, want true", eventType)
		}
	}
	if xaiIsTerminalResponseEventType("response.created") {
		t.Fatal("created should not be terminal")
	}
}
