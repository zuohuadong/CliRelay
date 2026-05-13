package responses

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexResponsesImageBridgeModel = "gpt-5.4-mini"

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	rawJSON = normalizeOpenAIResponsesImageRequest(rawJSON)

	inputResult := gjson.GetBytes(rawJSON, "input")
	if inputResult.Type == gjson.String {
		input, _ := sjson.Set(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`, "0.content.0.text", inputResult.String())
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", []byte(input))
	}

	rawJSON, _ = sjson.SetBytes(rawJSON, "stream", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "store", false)
	rawJSON, _ = sjson.SetBytes(rawJSON, "parallel_tool_calls", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "include", []string{"reasoning.encrypted_content"})
	// Codex Responses rejects token limit fields, so strip them out before forwarding.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_output_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_completion_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "temperature")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "top_p")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "service_tier")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "truncation")
	rawJSON = applyResponsesCompactionCompatibility(rawJSON)

	// Delete the user field as it is not supported by the Codex upstream.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "user")

	// Convert role "system" to "developer" in input array to comply with Codex API requirements.
	rawJSON = convertSystemRoleToDeveloper(rawJSON)

	return rawJSON
}

func normalizeOpenAIResponsesImageRequest(rawJSON []byte) []byte {
	rawJSON = normalizeOpenAIResponsesPromptInput(rawJSON)

	requestModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	isImageOnlyModel := isOpenAIResponsesImageOnlyModel(requestModel)
	toolIndex := firstImageGenerationToolIndex(rawJSON)
	if toolIndex < 0 && !isImageOnlyModel {
		return rawJSON
	}

	if toolIndex < 0 {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "tools", []byte(`[]`))
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "tools.-1", []byte(`{"type":"image_generation"}`))
		toolIndex = 0
	}

	toolModelPath := fmt.Sprintf("tools.%d.model", toolIndex)
	if isImageOnlyModel && strings.TrimSpace(gjson.GetBytes(rawJSON, toolModelPath).String()) == "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, toolModelPath, requestModel)
	}

	for _, field := range []string{
		"size",
		"quality",
		"background",
		"output_format",
		"output_compression",
		"moderation",
		"style",
		"partial_images",
	} {
		if !gjson.GetBytes(rawJSON, field).Exists() {
			continue
		}
		toolFieldPath := fmt.Sprintf("tools.%d.%s", toolIndex, field)
		if gjson.GetBytes(rawJSON, toolFieldPath).Exists() {
			rawJSON, _ = sjson.DeleteBytes(rawJSON, field)
			continue
		}
		rawJSON, _ = sjson.SetRawBytes(rawJSON, toolFieldPath, []byte(gjson.GetBytes(rawJSON, field).Raw))
		rawJSON, _ = sjson.DeleteBytes(rawJSON, field)
	}

	if isImageOnlyModel {
		if !gjson.GetBytes(rawJSON, "tool_choice").Exists() {
			rawJSON, _ = sjson.SetRawBytes(rawJSON, "tool_choice", []byte(`{"type":"image_generation"}`))
		}
		rawJSON, _ = sjson.SetBytes(rawJSON, "model", codexResponsesImageBridgeModel)
	}

	return rawJSON
}

func normalizeOpenAIResponsesPromptInput(rawJSON []byte) []byte {
	if gjson.GetBytes(rawJSON, "input").Exists() {
		return rawJSON
	}
	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	if prompt == "" {
		return rawJSON
	}
	rawJSON, _ = sjson.SetBytes(rawJSON, "input", prompt)
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "prompt")
	return rawJSON
}

func firstImageGenerationToolIndex(rawJSON []byte) int {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() {
		return -1
	}
	for index, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == "image_generation" {
			return index
		}
	}
	return -1
}

func isOpenAIResponsesImageOnlyModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-image-")
}

// applyResponsesCompactionCompatibility handles OpenAI Responses context_management.compaction
// for Codex upstream compatibility.
//
// Codex /responses currently rejects context_management with:
// {"detail":"Unsupported parameter: context_management"}.
//
// Compatibility strategy:
// 1) Remove context_management before forwarding to Codex upstream.
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
func convertSystemRoleToDeveloper(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputArray := inputResult.Array()
	result := rawJSON

	// Directly modify role values for items with "system" role
	for i := 0; i < len(inputArray); i++ {
		rolePath := fmt.Sprintf("input.%d.role", i)
		if gjson.GetBytes(result, rolePath).String() == "system" {
			result, _ = sjson.SetBytes(result, rolePath, "developer")
		}
	}

	return result
}
