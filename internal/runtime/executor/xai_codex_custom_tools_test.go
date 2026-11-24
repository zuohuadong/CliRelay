package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// xaiCodexCustomToolRequest is what Codex Desktop sent for grok-4.7 on
// 2026-11-24: a freeform apply_patch tool, a replayed custom call, its output,
// and an inter-agent message. xAI answered
// `422 tools[6].type: unknown variant `custom“.
const xaiCodexCustomToolRequest = `{
	"model":"grok-4.7",
	"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]},
		{"type":"custom_tool_call","call_id":"call_prev","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},
		{"type":"custom_tool_call_output","call_id":"call_prev","output":"Done"},
		{"type":"agent_message","author":"/root","recipient":"/root/executor","content":[
			{"type":"input_text","text":"Message Type: NEW_TASK"},
			{"type":"encrypted_content","encrypted_content":"gAAAA-opaque"}
		]}
	],
	"tools":[
		{"type":"function","name":"exec_command","description":"Run a command.","strict":false,"parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}},
		{"type":"custom","name":"apply_patch","description":"Use the apply_patch tool to edit files.","format":{"type":"grammar","syntax":"lark","definition":"start: begin_patch"}},
		{"type":"web_search"}
	],
	"tool_choice":{"type":"custom","name":"apply_patch"},
	"stream":true,
	"store":false
}`

func TestXAINormalizeUnsupportedCodexShapesConvertsCustomTools(t *testing.T) {
	body, names := xaiNormalizeUnsupportedCodexShapes([]byte(xaiCodexCustomToolRequest), nil)
	if names == nil || !names.isCustomName("apply_patch") {
		t.Fatalf("apply_patch was not recorded as a custom tool: %#v", names)
	}

	tools := gjson.GetBytes(body, "tools")
	if got := tools.Get("1.type").String(); got != "function" {
		t.Fatalf("tools[1].type = %q, want function", got)
	}
	if got := tools.Get("1.name").String(); got != "apply_patch" {
		t.Fatalf("tools[1].name = %q", got)
	}
	if !strings.Contains(tools.Get("1.description").String(), "start: begin_patch") {
		t.Fatalf("freeform format missing from description: %q", tools.Get("1.description").String())
	}
	if got := tools.Get("1.parameters.properties.input.type").String(); got != "string" {
		t.Fatalf("tools[1].parameters.input.type = %q", got)
	}
	if tools.Get("1.format").Exists() {
		t.Fatalf("custom format survived on the function: %s", tools.Get("1.Raw").String())
	}
	if got := gjson.GetBytes(body, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function", got)
	}

	input := gjson.GetBytes(body, "input")
	if got := input.Get("1.type").String(); got != "function_call" {
		t.Fatalf("input[1].type = %q, want function_call", got)
	}
	if got := input.Get("1.arguments").Type; got != gjson.String {
		t.Fatalf("input[1].arguments has type %v, want a JSON string", got)
	}
	if got := gjson.Get(input.Get("1.arguments").String(), "input").String(); got != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("input[1].arguments input = %q (arguments %q)", got, input.Get("1.arguments").String())
	}
	if got := input.Get("2.type").String(); got != "function_call_output" {
		t.Fatalf("input[2].type = %q, want function_call_output", got)
	}
	agent := input.Get("3")
	if got := agent.Get("type").String(); got != "message" {
		t.Fatalf("input[3].type = %q, want message", got)
	}
	if got := agent.Get("role").String(); got != "user" {
		t.Fatalf("input[3].role = %q, want user", got)
	}
	if agent.Get("author").Exists() || agent.Get("recipient").Exists() {
		t.Fatalf("agent addressing survived: %s", agent.Raw)
	}
	if got := agent.Get("content.#").Int(); got != 1 {
		t.Fatalf("input[3].content has %d parts, want 1: %s", got, agent.Raw)
	}
	if strings.Contains(string(body), "gAAAA-opaque") {
		t.Fatalf("encrypted agent content survived into the request")
	}
	if got := input.Get("0").Raw; !strings.Contains(got, `"fix it"`) {
		t.Fatalf("user message changed: %s", got)
	}
}

func TestXAINormalizeUnsupportedCodexShapesLeavesOtherRequestsAlone(t *testing.T) {
	body := []byte(`{"model":"grok-4.7","input":[{"type":"message","role":"user","content":[]}],"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}}]}`)
	got, names := xaiNormalizeUnsupportedCodexShapes(body, nil)
	if names != nil {
		t.Fatalf("names = %#v, want nil for a request with nothing to rewrite", names)
	}
	if string(got) != string(body) {
		t.Fatalf("body changed:\n got: %s\nwant: %s", got, body)
	}
}

func TestXAINamespaceToolNamesRestoresCustomToolEvents(t *testing.T) {
	_, names := xaiNormalizeUnsupportedCodexShapes([]byte(xaiCodexCustomToolRequest), nil)

	done := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\\n*** End Patch\"}"}}`)
	restored, changed := names.restoreEvent(done)
	if !changed {
		t.Fatalf("custom call was not restored")
	}
	item := gjson.GetBytes(restored, "item")
	if got := item.Get("type").String(); got != "custom_tool_call" {
		t.Fatalf("item.type = %q, want custom_tool_call", got)
	}
	if got := item.Get("input").String(); got != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("item.input = %q", got)
	}
	if item.Get("arguments").Exists() {
		t.Fatalf("arguments survived: %s", item.Raw)
	}

	completed := []byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","call_id":"call_1","name":"apply_patch","arguments":"{\"input\":\"patch\"}"},{"type":"function_call","call_id":"call_2","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"}]}}`)
	restored, changed = names.restoreEvent(completed)
	if !changed {
		t.Fatalf("completed output was not restored")
	}
	if got := gjson.GetBytes(restored, "response.output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("output[0].type = %q, want custom_tool_call", got)
	}
	other := gjson.GetBytes(restored, "response.output.1")
	if got := other.Get("type").String(); got != "function_call" {
		t.Fatalf("unrelated call rewritten to %q", got)
	}
	if got := other.Get("arguments").String(); got != `{"cmd":"ls"}` {
		t.Fatalf("unrelated arguments changed: %q", got)
	}

	// xAI names the item on argument events more often than the tool.
	added := []byte(`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_9","call_id":"call_9","name":"apply_patch","arguments":""}}`)
	if _, changed := names.restoreEvent(added); !changed {
		t.Fatalf("custom item.added was not restored")
	}
	if line := names.restoreSSELine([]byte(`data: {"type":"response.function_call_arguments.delta","item_id":"fc_9","delta":"{\"inp"}`)); line != nil {
		t.Fatalf("custom argument delta keyed only by item id survived as %q", line)
	}

	delta := []byte(`data: {"type":"response.function_call_arguments.delta","name":"apply_patch","delta":"{\"inp"}`)
	if line := names.restoreSSELine(delta); line != nil {
		t.Fatalf("custom argument delta survived as %q", line)
	}
	otherDelta := []byte(`data: {"type":"response.function_call_arguments.delta","name":"exec_command","delta":"{\"cmd"}`)
	if line := names.restoreSSELine(otherDelta); line == nil {
		t.Fatalf("unrelated argument delta was dropped")
	}

	// The event: line must go with the data line it introduced, and survive
	// together when the call belongs to another tool.
	_, holder := xaiNormalizeUnsupportedCodexShapes([]byte(xaiCodexCustomToolRequest), nil)
	if line := holder.restoreSSELine([]byte("event: response.function_call_arguments.delta")); line != nil {
		t.Fatalf("argument event line was forwarded before its data was judged: %q", line)
	}
	if line := holder.restoreSSELine([]byte(`data: {"type":"response.function_call_arguments.delta","name":"apply_patch","delta":"x"}`)); line != nil {
		t.Fatalf("custom argument event survived as %q", line)
	}
	if line := holder.restoreSSELine([]byte("event: response.function_call_arguments.delta")); line != nil {
		t.Fatalf("second argument event line was forwarded: %q", line)
	}
	kept := holder.restoreSSELine([]byte(`data: {"type":"response.function_call_arguments.delta","name":"exec_command","delta":"x"}`))
	if !strings.HasPrefix(string(kept), "event: response.function_call_arguments.delta\ndata: ") {
		t.Fatalf("unrelated argument event lost its event line: %q", kept)
	}
}
func TestXAINamespaceToolNamesKeepsArgumentsForTheFinishedItem(t *testing.T) {
	_, names := xaiNormalizeUnsupportedCodexShapes([]byte(xaiCodexCustomToolRequest), nil)

	done := []byte(`{"type":"response.function_call_arguments.done","item_id":"fc_1","name":"apply_patch","arguments":"{\"input\":\"patch from deltas\"}"}`)
	if restored, changed := names.restoreEvent(done); !changed || restored != nil {
		t.Fatalf("arguments.done was not consumed: changed=%v payload=%s", changed, restored)
	}

	item := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"apply_patch","arguments":""}}`)
	restored, changed := names.restoreEvent(item)
	if !changed {
		t.Fatalf("finished item was not restored")
	}
	got := gjson.GetBytes(restored, "item.input").String()
	if got != "patch from deltas" {
		t.Fatalf("item.input = %q, want the arguments from the done event", got)
	}
}
