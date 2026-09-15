package executor

func xaiIsKeepaliveEventType(eventType string) bool {
	switch eventType {
	case "keepalive", "response.keepalive", "ping", "response.ping", "heartbeat", "response.heartbeat":
		return true
	default:
		return false
	}
}

func xaiIsTerminalResponseEventType(eventType string) bool {
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.failed", "error":
		return true
	default:
		return false
	}
}
