package responses

import (
	"encoding/json"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON

	inputResult := gjson.GetBytes(rawJSON, "input")
	if inputResult.Type == gjson.String {
		input, _ := sjson.SetBytes([]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`), "0.content.0.text", inputResult.String())
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", input)
	}

	// Batch all set/delete operations via unmarshal-modify-marshal to avoid
	// O(n*m) allocations from sequential sjson calls (each creates a full copy).
	var obj map[string]any
	if err := json.Unmarshal(rawJSON, &obj); err != nil {
		// Fallback to original if we cannot parse.
		return rawJSON
	}

	// Set fields.
	obj["stream"] = true
	obj["store"] = false
	obj["parallel_tool_calls"] = true
	obj["include"] = []string{"reasoning.encrypted_content"}

	// Delete fields that Codex upstream rejects.
	delete(obj, "max_output_tokens")
	delete(obj, "max_completion_tokens")
	delete(obj, "temperature")
	delete(obj, "top_p")
	delete(obj, "truncation")
	delete(obj, "user")

	// Conditional deletes.
	if v, ok := obj["service_tier"]; ok {
		s, _ := v.(string)
		if s != "priority" {
			delete(obj, "service_tier")
		}
	}
	delete(obj, "context_management")

	// Convert role "system" to "developer" in input array.
	if input, ok := obj["input"]; ok {
		obj["input"] = convertSystemRoleInInput(input)
	}

	// Normalize built-in tool types.
	if tools, ok := obj["tools"]; ok {
		obj["tools"] = normalizeBuiltinToolsInArray(tools)
	}
	if tc, ok := obj["tool_choice"]; ok {
		obj["tool_choice"] = normalizeToolChoice(tc)
	}

	out, err := json.Marshal(obj)
	if err != nil {
		return rawJSON
	}
	return out
}

// convertSystemRoleInInput converts role "system" to "developer" in the input
// array without repeated sjson.SetBytes calls on the full payload.
func convertSystemRoleInInput(input any) any {
	items, ok := input.([]any)
	if !ok {
		return input
	}
	changed := false
	result := make([]any, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			result[i] = item
			continue
		}
		if role, _ := m["role"].(string); role == "system" {
			cp := make(map[string]any, len(m))
			for k, v := range m {
				cp[k] = v
			}
			cp["role"] = "developer"
			result[i] = cp
			changed = true
		} else {
			result[i] = item
		}
	}
	if !changed {
		return input
	}
	return result
}

// normalizeBuiltinToolsInArray normalizes legacy tool type names in the tools
// array without repeated sjson.SetBytes calls on the full payload.
func normalizeBuiltinToolsInArray(tools any) any {
	items, ok := tools.([]any)
	if !ok {
		return tools
	}
	changed := false
	result := make([]any, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			result[i] = item
			continue
		}
		currentType, _ := m["type"].(string)
		normalizedType := normalizeCodexBuiltinToolType(currentType)
		if normalizedType != "" {
			cp := make(map[string]any, len(m))
			for k, v := range m {
				cp[k] = v
			}
			cp["type"] = normalizedType
			result[i] = cp
			changed = true
			log.Debugf("codex responses: normalized builtin tool type from %q to %q", currentType, normalizedType)
		} else {
			result[i] = item
		}
	}
	if !changed {
		return tools
	}
	return result
}

// normalizeToolChoice normalizes tool_choice.type and tool_choice.tools[].type
// without repeated sjson calls on the full payload.
func normalizeToolChoice(tc any) any {
	m, ok := tc.(map[string]any)
	if !ok {
		return tc
	}
	changed := false
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}

	// Normalize top-level type.
	currentType, _ := cp["type"].(string)
	normalizedType := normalizeCodexBuiltinToolType(currentType)
	if normalizedType != "" {
		cp["type"] = normalizedType
		changed = true
		log.Debugf("codex responses: normalized builtin tool type at tool_choice.type from %q to %q", currentType, normalizedType)
	}

	// Normalize tool_choice.tools[].type.
	if tools, ok := cp["tools"].([]any); ok {
		toolsChanged := false
		toolsResult := make([]any, len(tools))
		for i, tool := range tools {
			tm, ok := tool.(map[string]any)
			if !ok {
				toolsResult[i] = tool
				continue
			}
			t, _ := tm["type"].(string)
			nt := normalizeCodexBuiltinToolType(t)
			if nt != "" {
				tcp := make(map[string]any, len(tm))
				for k, v := range tm {
					tcp[k] = v
				}
				tcp["type"] = nt
				toolsResult[i] = tcp
				toolsChanged = true
				log.Debugf("codex responses: normalized builtin tool type at tool_choice.tools from %q to %q", t, nt)
			} else {
				toolsResult[i] = tool
			}
		}
		if toolsChanged {
			cp["tools"] = toolsResult
			changed = true
		}
	}

	if !changed {
		return tc
	}
	return cp
}

// normalizeCodexBuiltinToolType centralizes the current known Codex Responses
// built-in tool alias compatibility. If Codex introduces more legacy aliases,
// extend this helper instead of adding path-specific rewrite logic elsewhere.
func normalizeCodexBuiltinToolType(toolType string) string {
	switch toolType {
	case "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}

// applyResponsesCompactionCompatibility handles OpenAI Responses context_management.compaction
// for Codex upstream compatibility.
//
// Codex /responses currently rejects context_management with:
// {"detail":"Unsupported parameter: context_management"}.
//
// Compatibility strategy:
// 1) Remove context_management before forwarding to Codex upstream.
//
// Deprecated: compaction is now removed inline by ConvertOpenAIResponsesRequestToCodex.
func applyResponsesCompactionCompatibility(rawJSON []byte) []byte {
	if !gjson.GetBytes(rawJSON, "context_management").Exists() {
		return rawJSON
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "context_management")
	return rawJSON
}

// convertSystemRoleToDeveloper traverses the input array and converts any message items
// with role "system" to role "developer". This is necessary because Codex API does not
// accept "system" role in the input array.
//
// Deprecated: system-to-developer conversion is now performed inline by
// ConvertOpenAIResponsesRequestToCodex via convertSystemRoleInInput.
func convertSystemRoleToDeveloper(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputItems := inputResult.Array()
	if len(inputItems) == 0 {
		return rawJSON
	}

	changed := false
	rebuiltInput := make([]json.RawMessage, 0, len(inputItems))
	for _, item := range inputItems {
		itemRaw := []byte(item.Raw)
		if item.IsObject() && item.Get("role").String() == "system" {
			updatedItem, errSetItem := sjson.SetRawBytes(itemRaw, "role", []byte(`"developer"`))
			if errSetItem != nil {
				return rawJSON
			}
			itemRaw = updatedItem
			changed = true
		}
		rebuiltInput = append(rebuiltInput, json.RawMessage(itemRaw))
	}
	if !changed {
		return rawJSON
	}

	inputRaw, errMarshalInput := json.Marshal(rebuiltInput)
	if errMarshalInput != nil {
		return rawJSON
	}
	updated, errSetInput := sjson.SetRawBytes(rawJSON, "input", inputRaw)
	if errSetInput != nil {
		return rawJSON
	}
	return updated
}

// normalizeCodexBuiltinTools rewrites legacy/preview built-in tool variants to the
// stable names expected by the current Codex upstream.
//
// Deprecated: tool normalization is now performed inline by
// ConvertOpenAIResponsesRequestToCodex via normalizeBuiltinToolsInArray.
func normalizeCodexBuiltinTools(rawJSON []byte) []byte {
	result := rawJSON

	tools := gjson.GetBytes(result, "tools")
	if tools.IsArray() {
		toolArray := tools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tools.%d.type", i)
			result = normalizeCodexBuiltinToolAtPath(result, typePath)
		}
	}

	result = normalizeCodexBuiltinToolAtPath(result, "tool_choice.type")

	toolChoiceTools := gjson.GetBytes(result, "tool_choice.tools")
	if toolChoiceTools.IsArray() {
		toolArray := toolChoiceTools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tool_choice.tools.%d.type", i)
			result = normalizeCodexBuiltinToolAtPath(result, typePath)
		}
	}

	return result
}

func normalizeCodexBuiltinToolAtPath(rawJSON []byte, path string) []byte {
	currentType := gjson.GetBytes(rawJSON, path).String()
	normalizedType := normalizeCodexBuiltinToolType(currentType)
	if normalizedType == "" {
		return rawJSON
	}

	updated, err := sjson.SetBytes(rawJSON, path, normalizedType)
	if err != nil {
		return rawJSON
	}

	log.Debugf("codex responses: normalized builtin tool type at %s from %q to %q", path, currentType, normalizedType)
	return updated
}
