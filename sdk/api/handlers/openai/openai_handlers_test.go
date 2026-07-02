package openai

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeChatCompletionsToolMessageHistoryDropsUnpairedItems(t *testing.T) {
	raw := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"start"},{"role":"tool","tool_call_id":"call-orphan","content":"missing call"},{"role":"assistant","tool_calls":[{"id":"call-unanswered","type":"function","function":{"name":"exec_command","arguments":"{}"}}]},{"role":"assistant","tool_calls":[{"id":"call-ok","type":"function","function":{"name":"exec_command","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-ok","content":"done"},{"role":"user","content":"next"}]}`)

	sanitized := sanitizeChatCompletionsToolMessageHistory(raw)

	messages := gjson.GetBytes(sanitized, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages len = %d, want 4: %s", len(messages), sanitized)
	}
	if messages[0].Get("role").String() != "user" || messages[1].Get("tool_calls.0.id").String() != "call-ok" || messages[2].Get("tool_call_id").String() != "call-ok" || messages[3].Get("role").String() != "user" {
		t.Fatalf("unexpected sanitized messages: %s", sanitized)
	}
	if strings.Contains(string(sanitized), "call-orphan") || strings.Contains(string(sanitized), "call-unanswered") {
		t.Fatalf("unpaired tool history leaked through: %s", sanitized)
	}
}

func TestSanitizeChatCompletionsToolMessageHistoryNormalizesCallID(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"exec_command","arguments":"{}"}}]},{"role":"tool","call_id":"call-1","content":"done"}]}`)

	sanitized := sanitizeChatCompletionsToolMessageHistory(raw)

	if got := gjson.GetBytes(sanitized, "messages.1.tool_call_id").String(); got != "call-1" {
		t.Fatalf("tool_call_id = %q, want call-1: %s", got, sanitized)
	}
	if got := len(gjson.GetBytes(sanitized, "messages").Array()); got != 2 {
		t.Fatalf("messages len = %d, want 2: %s", got, sanitized)
	}
}
