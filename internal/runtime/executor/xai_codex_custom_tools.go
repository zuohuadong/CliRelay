package executor

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Codex declares freeform (grammar-driven) tools as Responses `custom` tools:
// apply_patch, the unified exec tool, and anything an MCP server exposes as a
// freeform tool. xAI's Responses API knows no such declaration variant and
// refuses the whole request with
//
//	422 tools[N].type: unknown variant `custom`, expected one of `function`, ...
//
// The same endpoint also refuses Codex's multi-agent `agent_message` input
// items. So the request carries a custom tool as a function with one string
// argument, and the reply is handed back as the custom_tool_call the client
// knows: the item type, its input, and the stream events that carried it.

// xaiCustomToolInputDescription tells the model how to fill the stand-in
// function's only argument.
const xaiCustomToolInputDescription = "The complete raw input for this tool, exactly in the format the tool description specifies."

// xaiCustomToolParameters is the object schema a stand-in function advertises.
var xaiCustomToolParameters = []byte(`{"type":"object","properties":{"input":{"type":"string","description":"` +
	xaiCustomToolInputDescription + `"}},"required":["input"],"additionalProperties":false}`)

// xaiNormalizeUnsupportedCodexShapes rewrites the request shapes xAI answers
// with 422 and returns the names that have to be restored on the way back.
// Requests that carry none of them are returned byte for byte.
func xaiNormalizeUnsupportedCodexShapes(body []byte, names *xaiNamespaceToolNames) ([]byte, *xaiNamespaceToolNames) {
	if !bytes.Contains(body, []byte(`"custom"`)) &&
		!bytes.Contains(body, []byte(`"agent_message"`)) &&
		!bytes.Contains(body, []byte(`"custom_tool_call"`)) {
		return body, names
	}
	ensureNames := func() *xaiNamespaceToolNames {
		if names == nil {
			names = newXAINamespaceToolNames(map[string]struct{}{})
		}
		return names
	}

	convertedTools := 0
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
		for index, tool := range tools.Array() {
			if tool.Get("type").String() != "custom" {
				continue
			}
			updated, err := xaiCustomToolAsFunction(tool)
			if err != nil {
				continue
			}
			body, err = sjson.SetRawBytes(body, "tools."+strconv.Itoa(index), updated)
			if err != nil {
				continue
			}
			ensureNames().markCustom(tool.Get("name").String())
			convertedTools++
		}
	}
	if convertedTools > 0 {
		// A forced or allowed tool may name a declaration that just changed type.
		body = xaiCustomToolChoiceAsFunction(body, names)
	}

	if input := gjson.GetBytes(body, "input"); input.IsArray() {
		items := make([][]byte, 0, len(input.Array()))
		changed := false
		for _, item := range input.Array() {
			switch item.Get("type").String() {
			case "custom_tool_call":
				if raw, ok := xaiCustomToolCallAsFunctionCall(item); ok {
					items = append(items, raw)
					changed = true
					continue
				}
			case "custom_tool_call_output":
				if raw, err := sjson.SetBytes([]byte(item.Raw), "type", "function_call_output"); err == nil {
					items = append(items, raw)
					changed = true
					continue
				}
			case "agent_message":
				// Codex's inter-agent message is an input message to the model.
				if raw, keep := xaiAgentMessageAsMessage(item); keep {
					items = append(items, raw)
				}
				changed = true
				continue
			}
			items = append(items, []byte(item.Raw))
		}
		if changed {
			if updated, err := sjson.SetRawBytes(body, "input", xaiJoinRawJSONArray(items)); err == nil {
				body = updated
			}
		}
	}

	if convertedTools > 0 {
		log.Debugf("xai: sent %d custom tool(s) as functions; this endpoint has no custom declaration", convertedTools)
	}
	return body, names
}

// xaiCustomToolAsFunction turns one custom tool declaration into the function
// stand-in, folding the freeform format into the description so the model still
// knows what the argument has to look like.
func xaiCustomToolAsFunction(tool gjson.Result) ([]byte, error) {
	out := []byte(`{"type":"function"}`)
	out, err := sjson.SetBytes(out, "name", tool.Get("name").String())
	if err != nil {
		return nil, err
	}
	description := strings.TrimSpace(tool.Get("description").String())
	if definition := strings.TrimSpace(tool.Get("format.definition").String()); definition != "" {
		if description != "" {
			description += "\n\n"
		}
		description += "Input format:\n" + definition
	}
	if description != "" {
		out, err = sjson.SetBytes(out, "description", description)
		if err != nil {
			return nil, err
		}
	}
	out, err = sjson.SetBytes(out, "strict", false)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(out, "parameters", xaiCustomToolParameters)
}

// xaiCustomToolChoiceAsFunction moves a tool_choice or allowed_tools reference
// off the custom type now that its declaration is a function.
func xaiCustomToolChoiceAsFunction(body []byte, names *xaiNamespaceToolNames) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() {
		return body
	}
	switch choice.Get("type").String() {
	case "custom":
		if names == nil || !names.isCustomName(choice.Get("name").String()) {
			return body
		}
		if updated, err := sjson.SetBytes(body, "tool_choice.type", "function"); err == nil {
			return updated
		}
	case "allowed_tools":
		entries := choice.Get("tools")
		if !entries.IsArray() {
			return body
		}
		out := make([][]byte, 0, len(entries.Array()))
		for _, entry := range entries.Array() {
			if entry.Get("type").String() == "custom" {
				if raw, err := sjson.SetBytes([]byte(entry.Raw), "type", "function"); err == nil {
					entry = gjson.ParseBytes(raw)
				}
			}
			out = append(out, []byte(entry.Raw))
		}
		if updated, err := sjson.SetRawBytes(body, "tool_choice.tools", xaiJoinRawJSONArray(out)); err == nil {
			return updated
		}
	}
	return body
}

// xaiCustomToolCallAsFunctionCall replays a custom call from history as the
// function call the declaration now describes.
func xaiCustomToolCallAsFunctionCall(item gjson.Result) ([]byte, bool) {
	args, err := sjson.SetRawBytes([]byte(`{}`), "input", xaiCustomInputArgumentJSON(item.Get("input")))
	if err != nil {
		return nil, false
	}
	out, err := sjson.SetRawBytes([]byte(item.Raw), "arguments", args)
	if err != nil {
		return nil, false
	}
	out, err = sjson.SetBytes(out, "type", "function_call")
	if err != nil {
		return nil, false
	}
	out, err = sjson.DeleteBytes(out, "input")
	if err != nil {
		return nil, false
	}
	return out, true
}

// xaiCustomInputArgumentJSON encodes a custom tool's raw input as the value of
// the stand-in function's input argument.
func xaiCustomInputArgumentJSON(input gjson.Result) []byte {
	if !input.Exists() {
		return []byte(`""`)
	}
	if input.Type == gjson.String {
		return util.JSONString(input.String())
	}
	if input.Raw == "" {
		return []byte(`""`)
	}
	return []byte(input.Raw)
}

// xaiAgentMessageAsMessage replays one inter-agent message as an input message.
// Encrypted reasoning content has no place in a Grok message and is dropped; a
// message left with no text carries nothing and is dropped entirely.
func xaiAgentMessageAsMessage(item gjson.Result) ([]byte, bool) {
	parts := make([][]byte, 0, 2)
	if content := item.Get("content"); content.IsArray() {
		for _, part := range content.Array() {
			switch part.Get("type").String() {
			case "input_text", "text", "output_text":
				text := part.Get("text").String()
				if strings.TrimSpace(text) == "" {
					continue
				}
				if raw, err := sjson.SetBytes([]byte(`{"type":"input_text"}`), "text", text); err == nil {
					parts = append(parts, raw)
				}
			case "input_image", "input_file":
				parts = append(parts, []byte(part.Raw))
			}
		}
	}
	if len(parts) == 0 {
		return nil, false
	}
	out, err := sjson.SetBytes([]byte(item.Raw), "type", "message")
	if err != nil {
		return nil, false
	}
	out, err = sjson.SetBytes(out, "role", "user")
	if err != nil {
		return nil, false
	}
	out, err = sjson.SetRawBytes(out, "content", xaiJoinRawJSONArray(parts))
	if err != nil {
		return nil, false
	}
	out, _ = sjson.DeleteBytes(out, "author")
	out, _ = sjson.DeleteBytes(out, "recipient")
	return out, true
}

// restoreCustomToolEvent turns the stand-in function call back into the custom
// tool call Codex dispatched. It also drops the argument deltas: the finished
// item already carries the whole input.
func (n *xaiNamespaceToolNames) restoreCustomToolEvent(payload []byte) ([]byte, bool, bool) {
	if n == nil || len(n.custom) == 0 || len(payload) == 0 {
		return payload, false, false
	}
	switch gjson.GetBytes(payload, "type").String() {
	case "response.output_item.added", "response.output_item.done":
		item := gjson.GetBytes(payload, "item")
		if !n.isCustomName(item.Get("name").String()) {
			return payload, false, false
		}
		itemRaw := []byte(item.Raw)
		if strings.TrimSpace(item.Get("arguments").String()) == "" {
			if arguments := n.takeCustomArguments(item.Get("id").String(), item.Get("name").String()); arguments != "" {
				if withArguments, err := sjson.SetBytes(itemRaw, "arguments", arguments); err == nil {
					itemRaw = withArguments
				}
			}
		}
		updated, ok := xaiFunctionCallAsCustomToolCall(itemRaw)
		if !ok {
			return payload, false, false
		}
		out, err := sjson.SetRawBytes(payload, "item", updated)
		if err != nil {
			return payload, false, false
		}
		return out, true, false
	case "response.function_call_arguments.delta":
		if n.isCustomName(gjson.GetBytes(payload, "name").String()) {
			return nil, true, true
		}
		return payload, false, false
	case "response.function_call_arguments.done":
		if !n.isCustomName(gjson.GetBytes(payload, "name").String()) {
			return payload, false, false
		}
		n.rememberCustomArguments(
			gjson.GetBytes(payload, "item_id").String(),
			gjson.GetBytes(payload, "name").String(),
			gjson.GetBytes(payload, "arguments").String(),
		)
		return nil, true, true
	case "response.created", "response.in_progress", "response.completed", "response.incomplete", "response.failed":
		output := gjson.GetBytes(payload, "response.output")
		if !output.IsArray() {
			return payload, false, false
		}
		changed := false
		for index, item := range output.Array() {
			if !n.isCustomName(item.Get("name").String()) {
				continue
			}
			updated, ok := xaiFunctionCallAsCustomToolCall([]byte(item.Raw))
			if !ok {
				continue
			}
			next, err := sjson.SetRawBytes(payload, "response.output."+strconv.Itoa(index), updated)
			if err != nil {
				continue
			}
			payload, changed = next, true
		}
		return payload, changed, false
	}
	return payload, false, false
}

// xaiFunctionCallAsCustomToolCall rewrites one finished function call.
func xaiFunctionCallAsCustomToolCall(item []byte) ([]byte, bool) {
	if gjson.GetBytes(item, "type").String() != "function_call" {
		return item, false
	}
	out, err := sjson.SetBytes(item, "type", "custom_tool_call")
	if err != nil {
		return item, false
	}
	out, err = sjson.SetBytes(out, "input", xaiCustomInputFromArguments(gjson.GetBytes(item, "arguments").String()))
	if err != nil {
		return item, false
	}
	out, err = sjson.DeleteBytes(out, "arguments")
	if err != nil {
		return item, false
	}
	return out, true
}

// xaiCustomInputFromArguments unwraps the input argument the model filled. A
// reply that is not the advertised object is passed on as the raw input, which
// is the best thing the tool can be given.
func xaiCustomInputFromArguments(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return ""
	}
	input := gjson.Get(arguments, "input")
	if !input.Exists() {
		return arguments
	}
	if input.Type == gjson.String {
		return input.String()
	}
	return input.Raw
}
