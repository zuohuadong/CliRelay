package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func reasoningReplayItem(id, signature, text string) string {
	item := `{"type":"reasoning","id":"","summary":[{"type":"summary_text","text":""}],"encrypted_content":""}`
	item = strings.Replace(item, `"id":""`, `"id":"`+id+`"`, 1)
	item = strings.Replace(item, `"text":""`, `"text":"`+text+`"`, 1)
	return strings.Replace(item, `"encrypted_content":""`, `"encrypted_content":"`+signature+`"`, 1)
}

func replayRequestWithReasoning(t *testing.T, items ...string) []byte {
	t.Helper()
	body := `{"model":"claude-test","stream":true,"input":[` + strings.Join(items, ",") + `]}`
	if !gjson.Valid(body) {
		t.Fatalf("fixture is not valid JSON: %s", body)
	}
	return []byte(body)
}

func assistantContentTypes(t *testing.T, out []byte) []string {
	t.Helper()
	content := gjson.GetBytes(out, "messages.0.content")
	var types []string
	content.ForEach(func(_, part gjson.Result) bool {
		types = append(types, part.Get("type").String())
		return true
	})
	return types
}

// One Claude response can return a reasoning block and a later progress-update
// block. They belong to the same message and must be replayed in order:
// Anthropic only accepts adjacent thinking blocks of one response.
func TestReplayedThinkingBlocksOfOneResponseAreKept(t *testing.T) {
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", replayRequestWithReasoning(t,
		reasoningReplayItem("rs_msg_01Same_0", testClaudeSig, "primary reasoning"),
		reasoningReplayItem("rs_msg_01Same_1", testClaudeClassicSig, "progress update"),
		`{"type":"function_call","name":"shell","call_id":"call_1","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"call_1","output":"ok"}`,
	), false)

	types := assistantContentTypes(t, out)
	want := []string{"thinking", "thinking", "tool_use"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("content types = %v, want %v. Output: %s", types, want, out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.thinking").String(); got != "primary reasoning" {
		t.Fatalf("first thinking = %q", got)
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.thinking").String(); got != "progress update" {
		t.Fatalf("second thinking = %q", got)
	}
}

// Thinking blocks from two different Claude responses cannot sit next to each
// other, so only the latest is kept.
func TestReplayedThinkingBlocksOfDifferentResponsesCollapse(t *testing.T) {
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", replayRequestWithReasoning(t,
		reasoningReplayItem("rs_msg_01First_0", testClaudeSig, "first reasoning"),
		reasoningReplayItem("rs_msg_01First_1", testClaudeClassicSig, "first update"),
		reasoningReplayItem("rs_msg_01Later_0", testClaudeSig, "later reasoning"),
		`{"type":"function_call","name":"shell","call_id":"call_1","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"call_1","output":"ok"}`,
	), false)

	types := assistantContentTypes(t, out)
	want := []string{"thinking", "tool_use"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("content types = %v, want %v. Output: %s", types, want, out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.thinking").String(); got != "later reasoning" {
		t.Fatalf("surviving thinking = %q, want the latest response's", got)
	}
}

// A client that replays the same block twice (a retried turn) must not produce
// two blocks with one index.
func TestReplayedDuplicateThinkingBlockIndexReplacesIt(t *testing.T) {
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", replayRequestWithReasoning(t,
		reasoningReplayItem("rs_msg_01Dup_0", testClaudeSig, "first version"),
		reasoningReplayItem("rs_msg_01Dup_0", testClaudeClassicSig, "second version"),
		`{"type":"function_call","name":"shell","call_id":"call_1","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"call_1","output":"ok"}`,
	), false)

	types := assistantContentTypes(t, out)
	want := []string{"thinking", "tool_use"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("content types = %v, want %v. Output: %s", types, want, out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.thinking").String(); got != "second version" {
		t.Fatalf("thinking = %q, want the replayed block", got)
	}
}

// Out-of-order or unattributed ids cannot be proven to belong to one response,
// so the trailing run collapses to the latest block.
func TestReplayedThinkingBlocksWithoutUsableOriginCollapse(t *testing.T) {
	cases := map[string][2]string{
		"descending index":  {"rs_msg_01Order_2", "rs_msg_01Order_1"},
		"unattributed ids":  {"rs_a", "rs_b"},
		"empty response id": {"rs__0", "rs__1"},
		"signed index":      {"rs_msg_01Plus_+0", "rs_msg_01Plus_+1"},
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToClaude("claude-test", replayRequestWithReasoning(t,
				reasoningReplayItem(ids[0], testClaudeSig, "first"),
				reasoningReplayItem(ids[1], testClaudeClassicSig, "second"),
				`{"type":"function_call","name":"shell","call_id":"call_1","arguments":"{}"}`,
				`{"type":"function_call_output","call_id":"call_1","output":"ok"}`,
			), false)

			types := assistantContentTypes(t, out)
			want := []string{"thinking", "tool_use"}
			if strings.Join(types, ",") != strings.Join(want, ",") {
				t.Fatalf("content types = %v, want %v. Output: %s", types, want, out)
			}
			if got := gjson.GetBytes(out, "messages.0.content.0.thinking").String(); got != "second" {
				t.Fatalf("thinking = %q, want the latest block", got)
			}
		})
	}
}

func TestParseClaudeReasoningItemID(t *testing.T) {
	cases := []struct {
		id       string
		response string
		index    int
		ok       bool
	}{
		{id: "rs_msg_01ABC_0", response: "msg_01ABC", index: 0, ok: true},
		{id: "rs_msg_01ABC_12", response: "msg_01ABC", index: 12, ok: true},
		{id: "rs_msg_01_with_underscore_3", response: "msg_01_with_underscore", index: 3, ok: true},
		{id: "rs", ok: false},
		{id: "rs_", ok: false},
		{id: "rs__0", ok: false},
		{id: "rs_msg_01ABC", ok: false},
		{id: "rs_msg_01ABC_", ok: false},
		{id: "rs_msg_01ABC_-1", ok: false},
		{id: "rs_msg_01ABC_+0", ok: false},
		{id: "msg_01ABC_0", ok: false},
	}
	for _, tc := range cases {
		response, index, ok := parseClaudeReasoningItemID(tc.id)
		if ok != tc.ok {
			t.Fatalf("%q: ok = %v, want %v", tc.id, ok, tc.ok)
		}
		if !tc.ok {
			continue
		}
		if response != tc.response || index != tc.index {
			t.Fatalf("%q: got (%q,%d), want (%q,%d)", tc.id, response, index, tc.response, tc.index)
		}
	}
}
