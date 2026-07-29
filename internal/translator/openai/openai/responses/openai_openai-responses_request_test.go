package responses

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func prettyJSONForTest(raw []byte) string {
	if !gjson.ValidBytes(raw) {
		return string(raw)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergeConsecutiveFunctionCalls(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"exec_command:0","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call","call_id":"exec_command:1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"exec_command:0","output":"ok0"},
			{"type":"function_call_output","call_id":"exec_command:1","output":"ok1"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	msgs := gjson.GetBytes(out, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		t.Fatalf("messages should be an array")
	}
	if got := len(msgs.Array()); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}

	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := len(gjson.GetBytes(out, "messages.0.tool_calls").Array()); got != 2 {
		t.Fatalf("messages.0.tool_calls length = %d, want %d", got, 2)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "exec_command:0" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "exec_command:0")
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String(); got != "exec_command:1" {
		t.Fatalf("messages.0.tool_calls.1.id = %q, want %q", got, "exec_command:1")
	}

	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "exec_command:0" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "exec_command:0")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "exec_command:1" {
		t.Fatalf("messages.2.tool_call_id = %q, want %q", got, "exec_command:1")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DoesNotEmitEmptyToolCallID(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"message","role":"tool","content":"missing link"},
			{"type":"message","role":"tool","call_id":"call_1","content":"linked result"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("k3", raw, true)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1: %s", len(messages), out)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_call_id").String(); got != "call_1" {
		t.Fatalf("messages.0.tool_call_id = %q, want call_1: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_InfersSinglePendingToolCallID(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{}"},
			{"type":"message","role":"tool","content":"done"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("k3", raw, true)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want 2: %s", len(messages), out)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want call_1: %s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_1" {
		t.Fatalf("messages.1.tool_call_id = %q, want inferred call_1: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_InfersAwaitingToolCallID(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{}"},
			{"type":"message","role":"user","content":"approval recorded"},
			{"type":"message","role":"tool","content":"done"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("k3", raw, true)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3: %s", len(messages), out)
	}
	if got := messages[1].Get("tool_call_id").String(); got != "call_1" {
		t.Fatalf("messages.1.tool_call_id = %q, want inferred call_1: %s", got, out)
	}
	if got := messages[2].Get("role").String(); got != "user" {
		t.Fatalf("messages.2.role = %q, want deferred user: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DoesNotInferAcrossInvalidParallelCalls(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"","name":"exec_command","arguments":"{}"},
			{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"},
			{"type":"message","role":"tool","content":"ambiguous"},
			{"type":"message","role":"user","content":"continue"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("k3", raw, true)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 || messages[0].Get("role").String() != "user" {
		t.Fatalf("ambiguous tool history should be dropped: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DropsResultsForDiscardedParallelCalls(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_a","name":"exec_command","arguments":"{}"},
			{"type":"function_call","call_id":"call_b","name":"read_file","arguments":"{}"},
			{"type":"message","role":"tool","content":"ambiguous"},
			{"type":"message","role":"tool","call_id":"call_b","content":"late chat result"},
			{"type":"function_call_output","call_id":"call_a","output":"late native result"},
			{"type":"message","role":"user","content":"continue"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("k3", raw, true)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 || messages[0].Get("role").String() != "user" {
		t.Fatalf("discarded calls and late results should be dropped together: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_SplitFunctionCallsWhenInterrupted(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"message","role":"user","content":"next"},
			{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := len(gjson.GetBytes(out, "messages").Array()); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "call_a" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "call_a")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_calls.0.id").String(); got != "call_b" {
		t.Fatalf("messages.2.tool_calls.0.id = %q, want %q", got, "call_b")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DefersMessageUntilToolOutput(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_x","name":"exec_command","arguments":"{\"cmd\":\"echo hi\"}"},
			{"type":"message","role":"user","content":"Approved command prefix saved"},
			{"type":"function_call_output","call_id":"call_x","output":"ok"},
			{"type":"message","role":"user","content":"next"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := len(gjson.GetBytes(out, "messages").Array()); got != 4 {
		t.Fatalf("messages count = %d, want %d", got, 4)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want %q", got, "tool")
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_x" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_x")
	}
	if got := gjson.GetBytes(out, "messages.2.role").String(); got != "user" {
		t.Fatalf("messages.2.role = %q, want %q", got, "user")
	}
	if got := gjson.GetBytes(out, "messages.2.content").String(); got != "Approved command prefix saved" {
		t.Fatalf("messages.2.content = %q, want %q", got, "Approved command prefix saved")
	}
	if got := gjson.GetBytes(out, "messages.3.content").String(); got != "next" {
		t.Fatalf("messages.3.content = %q, want %q", got, "next")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesRequestUserInputTool(t *testing.T) {
	raw := []byte(`{
		"tools": [
			{"type":"function","name":"request_user_input","description":"Ask the user to choose","parameters":{"type":"object","properties":{"questions":{"type":"array"}}}}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("glm-5.1", raw, true)
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "request_user_input" {
		t.Fatalf("tools.0.function.name = %q, want request_user_input", got)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.type").String(); got != "object" {
		t.Fatalf("tools.0.function.parameters.type = %q, want object", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_AttachesReasoningToAssistantMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{
				"type": "reasoning",
				"id": "rs_1",
				"summary": [
					{"type": "summary_text", "text": "first line\n"},
					{"type": "summary_text", "text": "second line"}
				]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "answer"}]
			},
			{"type": "message", "role": "user", "content": "next"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.#").Int(); got != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "first line\nsecond line" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q; output=%s", got, "first line\nsecond line", out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "answer" {
		t.Fatalf("messages.0.content.0.text = %q, want answer; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "user" {
		t.Fatalf("messages.1.role = %q, want user; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_AttachesReasoningToToolCallMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{
				"type": "reasoning",
				"id": "rs_tool",
				"summary": [{"type": "summary_text", "text": "tool reasoning"}]
			},
			{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.#").Int(); got != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "tool reasoning" {
		t.Fatalf("messages.0.reasoning_content = %q, want tool reasoning; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want call_1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want tool; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_KeepsReasoningBeforeUserMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type": "reasoning", "id": "rs_empty", "summary": []},
			{"type": "message", "role": "user", "content": "continue"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.#").Int(); got != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "[reasoning unavailable]" {
		t.Fatalf("messages.0.reasoning_content = %q, want placeholder; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "user" {
		t.Fatalf("messages.1.role = %q, want user; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_FlattensNamespaceTools(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"Use add_numbers."}
		],
		"tools": [
			{
				"type": "namespace",
				"name": "mcp__test_mcp__",
				"description": "Tools in the mcp__test_mcp__ namespace.",
				"tools": [
					{
						"type": "function",
						"name": "add_numbers",
						"description": "Add two numbers",
						"parameters": {
							"type": "object",
							"properties": {
								"a": { "type": "number" },
								"b": { "type": "number" }
							},
							"required": ["a", "b"]
						}
					}
				]
			}
		],
		"tool_choice": "auto"
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "mcp__test_mcp__add_numbers" {
		t.Fatalf("tools.0.function.name = %q, want mcp__test_mcp__add_numbers; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.description").String(); got != "Add two numbers" {
		t.Fatalf("tools.0.function.description = %q, want Add two numbers; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.required.0").String(); got != "a" {
		t.Fatalf("tools.0.function.parameters.required.0 = %q, want a; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_FlattensNamespaceCustomTools(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "top-level tools",
			raw: []byte(`{
				"tools":[{
					"type":"namespace",
					"name":"terminal",
					"tools":[{"type":"custom","name":"exec","description":"Run a command"}]
				}]
			}`),
		},
		{
			name: "additional tools",
			raw: []byte(`{
				"input":[{
					"type":"additional_tools",
					"tools":[{
						"type":"namespace",
						"name":"terminal",
						"tools":[{"type":"custom","name":"exec","description":"Run a command"}]
					}]
				}]
			}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", tt.raw, false)

			if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
				t.Fatalf("tools count = %d, want 1; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "terminal__exec" {
				t.Fatalf("tool name = %q, want terminal__exec; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.description").String(); got != "Run a command" {
				t.Fatalf("tool description = %q, want Run a command; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.parameters.type").String(); got != "object" {
				t.Fatalf("parameters type = %q, want object; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.input.type").String(); got != "string" {
				t.Fatalf("input type = %q, want string; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.parameters.required.0").String(); got != "input" {
				t.Fatalf("required parameter = %q, want input; output=%s", got, out)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesStructuredToolChoice(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"Run command."}
		],
		"tool_choice": {
			"type": "function",
			"function": {
				"name": "run_command"
			}
		}
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != "run_command" {
		t.Fatalf("tool_choice.function.name = %q, want run_command; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesInputImageDetail(t *testing.T) {
	raw := []byte(`{
		"input": [
			{
				"role": "user",
				"content": [
					{
						"type": "input_image",
						"image_url": "https://example.com/image.png",
						"detail": "high"
					}
				]
			}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.url").String(); got != "https://example.com/image.png" {
		t.Fatalf("messages.0.content.0.image_url.url = %q, want https://example.com/image.png; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.detail").String(); got != "high" {
		t.Fatalf("messages.0.content.0.image_url.detail = %q, want high; output=%s", got, out)
	}
}

func TestConvertOpenAIChatCompletionsRequestToOpenAIResponses(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-pro",
		"messages":[
			{"role":"system","content":"be concise"},
			{"role":"user","content":[
				{"type":"text","text":"hello"},
				{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"high"}}
			]},
			{"role":"assistant","content":"working"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\\"q\\":\\"x\\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"result"}
		],
		"tools":[{"type":"function","function":{"name":"lookup","description":"look up a value","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}],
		"tool_choice":{"type":"function","function":{"name":"lookup"}},
		"max_tokens":32,
		"reasoning_effort":"high",
		"parallel_tool_calls":true,
		"stream":true
	}`)

	out := ConvertOpenAIChatCompletionsRequestToOpenAIResponses("deepseek-v4-pro", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "model").String(); got != "deepseek-v4-pro" {
		t.Fatalf("model = %q, want deepseek-v4-pro", got)
	}
	if gjson.GetBytes(out, "messages").Exists() {
		t.Fatalf("unexpected chat messages in responses request: %s", out)
	}
	if got := gjson.GetBytes(out, "input.0.role").String(); got != "developer" {
		t.Fatalf("input.0.role = %q, want developer", got)
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "be concise" {
		t.Fatalf("input.0.content.0.text = %q, want be concise", got)
	}
	if got := gjson.GetBytes(out, "input.1.role").String(); got != "user" {
		t.Fatalf("input.1.role = %q, want user", got)
	}
	if got := gjson.GetBytes(out, "input.1.content.0.type").String(); got != "input_text" {
		t.Fatalf("input.1.content.0.type = %q, want input_text", got)
	}
	if got := gjson.GetBytes(out, "input.1.content.1.type").String(); got != "input_image" {
		t.Fatalf("input.1.content.1.type = %q, want input_image", got)
	}
	if got := gjson.GetBytes(out, "input.1.content.1.detail").String(); got != "high" {
		t.Fatalf("input.1.content.1.detail = %q, want high", got)
	}
	if got := gjson.GetBytes(out, "input.3.type").String(); got != "function_call" {
		t.Fatalf("input.3.type = %q, want function_call", got)
	}
	if got := gjson.GetBytes(out, "input.3.call_id").String(); got != "call_1" {
		t.Fatalf("input.3.call_id = %q, want call_1", got)
	}
	if got := gjson.GetBytes(out, "input.4.type").String(); got != "function_call_output" {
		t.Fatalf("input.4.type = %q, want function_call_output", got)
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want lookup", got)
	}
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function", got)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.name = %q, want lookup", got)
	}
	if got := gjson.GetBytes(out, "max_output_tokens").Int(); got != 32 {
		t.Fatalf("max_output_tokens = %d, want 32", got)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high", got)
	}
	if !gjson.GetBytes(out, "parallel_tool_calls").Bool() || !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("expected parallel_tool_calls and stream to remain true: %s", out)
	}
}
