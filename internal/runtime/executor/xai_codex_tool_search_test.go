package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// Codex declares client tool search on grok requests, and xAI answers the
// whole turn with "tool_search execution must be 'server' or not set".
// Captured from Codex CLI 0.166.0 against grok-4.5 (kittors/CliRelay#696):
// the declaration sits at top level, the multi-agent namespace carries another
// one, a Lite additional_tools item repeats it, and the history replays an
// earlier search.
const xaiCodexToolSearchRequest = `{
	"model":"grok-4.5",
	"input":[
		{"type":"additional_tools","role":"developer","tools":[
			{"type":"tool_search","execution":"client"},
			{"type":"function","name":"list_tools","description":"List tools.","parameters":{"type":"object","properties":{}}}
		]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"find a tool"}]},
		{"type":"tool_search_call","call_id":"ts_1","execution":"client","arguments":{"query":"calendar"}},
		{"type":"tool_search_output","call_id":"ts_1","execution":"client","tools":[{"type":"function","name":"calendar_create"}]},
		{"type":"function_call","call_id":"call_prev","name":"list_tools","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_prev","output":"none"}
	],
	"tools":[
		{"type":"tool_search","execution":"client"},
		{"type":"function","name":"exec_command","description":"Run a command.","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
		{"type":"namespace","name":"multi_agent_v1","description":"Tools for spawning and managing sub-agents.","tools":[
			{"type":"tool_search","execution":"client"},
			{"type":"function","name":"spawn_agent","description":"Spawn a sub-agent.","parameters":{"type":"object","properties":{}}}
		]}
	],
	"tool_choice":{"type":"allowed_tools","tools":[
		{"type":"tool_search"},
		{"type":"function","name":"exec_command"}
	]},
	"stream":true
}`

func TestStripXAIUnsupportedToolSearchDropsClientDeclarations(t *testing.T) {
	body := stripXAIUnsupportedToolSearch([]byte(xaiCodexToolSearchRequest))

	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() == "tool_search" {
			t.Fatalf("tool_search declaration reached xAI: %s", body)
		}
		for _, nested := range tool.Get("tools").Array() {
			if nested.Get("type").String() == "tool_search" {
				t.Fatalf("nested tool_search declaration reached xAI: %s", body)
			}
		}
	}
	var toolNames []string
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() == "function" {
			toolNames = append(toolNames, tool.Get("name").String())
		}
	}
	if got := strings.Join(toolNames, ","); got != "exec_command" {
		t.Fatalf("top-level tools = %s, want [exec_command]", got)
	}
	if got := gjson.GetBytes(body, "tools.1.name").String(); got != "multi_agent_v1" {
		t.Fatalf("namespace tool moved: %s", gjson.GetBytes(body, "tools").Raw)
	}
	if got := gjson.GetBytes(body, "tools.1.tools.0.name").String(); got != "spawn_agent" {
		t.Fatalf("namespace child = %q, want spawn_agent", got)
	}

	var inputTypes []string
	for _, item := range gjson.GetBytes(body, "input").Array() {
		inputTypes = append(inputTypes, item.Get("type").String())
	}
	if got := strings.Join(inputTypes, ","); got != "additional_tools,message,function_call,function_call_output" {
		t.Fatalf("input item types = %s", got)
	}
	if got := gjson.GetBytes(body, "input.0.tools.#").Int(); got != 1 {
		t.Fatalf("additional_tools declarations = %d, want 1: %s", got, gjson.GetBytes(body, "input.0").Raw)
	}

	choice := gjson.GetBytes(body, "tool_choice")
	if got := choice.Get("tools.#").Int(); got != 1 {
		t.Fatalf("allowed_tools entries = %d, want 1: %s", got, choice.Raw)
	}
	if got := choice.Get("tools.0.name").String(); got != "exec_command" {
		t.Fatalf("kept allowed tool = %q, want exec_command", got)
	}
}

func TestStripXAIUnsupportedToolSearchDropsForcedChoice(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","tools":[{"type":"tool_search","execution":"client"}],"tool_choice":{"type":"tool_search"}}`)
	out := stripXAIUnsupportedToolSearch(body)
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("forced tool_search choice survived: %s", out)
	}
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 0 {
		t.Fatalf("tools = %s, want an empty array", gjson.GetBytes(out, "tools").Raw)
	}
}

func TestStripXAIUnsupportedToolSearchLeavesOtherRequestsAlone(t *testing.T) {
	for name, body := range map[string]string{
		"no tool_search": `{"model":"grok-4.5","input":[{"type":"message","role":"user","content":"hi"}],"tools":[{"type":"function","name":"exec_command"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			out := stripXAIUnsupportedToolSearch([]byte(body))
			if string(out) != body {
				t.Fatalf("request changed:\n got %s\nwant %s", out, body)
			}
		})
	}
}

// xAI runs server-side tool search itself, but a server declaration is still a
// different tool than the one Codex dispatches, so it goes away as well and
// the rest of the request stays untouched.
func TestStripXAIUnsupportedToolSearchDropsServerDeclarations(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":"hi","tools":[{"type":"tool_search"},{"type":"tool_search","execution":"server"}]}`)
	out := stripXAIUnsupportedToolSearch(body)
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 0 {
		t.Fatalf("tools = %s, want an empty array", gjson.GetBytes(out, "tools").Raw)
	}
	if got := gjson.GetBytes(out, "input").String(); got != "hi" {
		t.Fatalf("input = %q, want hi", got)
	}
}

// The whole pipeline has to drop tool search while still flattening what
// remains of the namespaces and Lite items.
func TestXAIPrepareResponsesRequestStripsToolSearch(t *testing.T) {
	payload := []byte(xaiCodexToolSearchRequest)
	_, body, _, err := NewXAIExecutor(&config.Config{}).prepareResponsesRequest(
		context.Background(),
		&cliproxyauth.Auth{Provider: "xai"},
		cliproxyexecutor.Request{Model: "grok-4.5", Payload: payload},
		cliproxyexecutor.Options{OriginalRequest: payload, SourceFormat: sdktranslator.FormatOpenAIResponse},
		false,
	)
	if err != nil {
		t.Fatalf("prepareResponsesRequest returned error: %v", err)
	}
	if strings.Contains(gjson.GetBytes(body, "tools").Raw, `"type":"tool_search"`) {
		t.Fatalf("tool_search declaration reached xAI: %s", gjson.GetBytes(body, "tools").Raw)
	}
	if strings.Contains(gjson.GetBytes(body, "input").Raw, "tool_search") {
		t.Fatalf("tool_search history item reached xAI: %s", gjson.GetBytes(body, "input").Raw)
	}
	var names []string
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() != "function" {
			t.Fatalf("non-function tool reached xAI: %s", tool.Raw)
		}
		names = append(names, tool.Get("name").String())
	}
	// Top-level tools keep their order; Lite declarations are lifted after them.
	if got := strings.Join(names, ","); got != "exec_command,multi_agent_v1__spawn_agent,list_tools" {
		t.Fatalf("outbound tools = %s", got)
	}
	// xAI has no allowed_tools variant at all, so the choice falls back to its
	// default auto instead of failing the turn.
	if choice := gjson.GetBytes(body, "tool_choice"); choice.Exists() {
		t.Fatalf("allowed_tools choice reached xAI: %s", choice.Raw)
	}
}

func TestStripXAIUnsupportedToolSearchDropsHistoryOnlyRequests(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":[{"type":"tool_search_call","call_id":"ts_1","execution":"client"},{"type":"tool_search_output","call_id":"ts_1","execution":"client"},{"type":"message","role":"user","content":"hi"}],"tools":[{"type":"function","name":"exec_command"}]}`)
	out := stripXAIUnsupportedToolSearch(body)
	var types []string
	for _, item := range gjson.GetBytes(out, "input").Array() {
		types = append(types, item.Get("type").String())
	}
	if got := strings.Join(types, ","); got != "message" {
		t.Fatalf("input item types = %s, want [message]", got)
	}
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tools = %s", gjson.GetBytes(out, "tools").Raw)
	}
}

func TestNormalizeXAIAllowedToolsChoiceFallsBackToAuto(t *testing.T) {
	body := []byte(`{"model":"grok-4.7","tools":[{"type":"function","name":"exec_command"}],"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"exec_command"}]}}`)
	out := normalizeXAIAllowedToolsChoice(body)
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("allowed_tools choice survived: %s", out)
	}
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tools = %s", gjson.GetBytes(out, "tools").Raw)
	}

	for name, untouched := range map[string]string{
		"auto":        `{"tool_choice":"auto"}`,
		"forced":      `{"tool_choice":{"type":"function","name":"exec_command"}}`,
		"none string": `{"tool_choice":"none"}`,
		"absent":      `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := string(normalizeXAIAllowedToolsChoice([]byte(untouched))); got != untouched {
				t.Fatalf("rewritten: %s", got)
			}
		})
	}
}
