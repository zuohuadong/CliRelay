package handlers

import (
	"encoding/json"
	"strings"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func enrichRequestExecutionMetadata(meta map[string]any, rawJSON []byte) {
	enrichRequestExecutionMetadataForAlt(meta, rawJSON, "")
}

func enrichRequestExecutionMetadataForAlt(meta map[string]any, rawJSON []byte, alt string) {
	if meta == nil || len(rawJSON) == 0 {
		return
	}
	inputItems := countTopLevelItems(rawJSON)
	toolDefinitions := countTopLevelTools(rawJSON)
	toolCalls := countJSONOccurrences(rawJSON, []string{"function_call", "tool_call", "function_call_output", "tool_result"})
	features := requestFeatures(rawJSON, inputItems, toolDefinitions, toolCalls)
	if alt == "responses/compact" {
		features = nil
	}
	meta[coreexecutor.InputItemsMetadataKey] = inputItems
	meta[coreexecutor.ToolDefinitionsMetadataKey] = toolDefinitions
	meta[coreexecutor.ToolCallsMetadataKey] = toolCalls
	if len(features) > 0 {
		meta[coreexecutor.RequestFeaturesMetadataKey] = features
	}
}

func countTopLevelItems(rawJSON []byte) int {
	for _, path := range []string{"input", "messages"} {
		result := gjson.GetBytes(rawJSON, path)
		if result.IsArray() {
			return len(result.Array())
		}
	}
	return 0
}

func countTopLevelTools(rawJSON []byte) int {
	result := gjson.GetBytes(rawJSON, "tools")
	if result.IsArray() {
		return len(result.Array())
	}
	return 0
}

func countJSONOccurrences(rawJSON []byte, needles []string) int {
	text := strings.ToLower(string(rawJSON))
	total := 0
	for _, needle := range needles {
		total += strings.Count(text, strings.ToLower(needle))
	}
	return total
}

func requestFeatures(rawJSON []byte, inputItems, toolDefinitions, toolCalls int) []string {
	features := make([]string, 0, 4)
	hasImage, hasFile, hasVideo := structuredMediaFeatures(rawJSON)
	if hasImage || hasFile || hasVideo {
		features = append(features, "multimodal")
	}
	if hasImage {
		features = append(features, "image")
	}
	if hasFile {
		features = append(features, "file")
	}
	if hasVideo {
		features = append(features, "video")
	}
	if toolDefinitions > 0 || toolCalls > 0 {
		features = append(features, "tools")
	}
	if hasRequiredToolChoice(rawJSON) {
		features = append(features, "required-tools")
	}
	if hasMCPTool(rawJSON) {
		features = append(features, "mcp")
	}
	if hasXunfeiUnsupportedTool(rawJSON) {
		features = append(features, "xunfei-unsupported-tools")
	}
	if toolCalls >= 16 {
		features = append(features, "tool-heavy")
	}
	if inputItems >= 80 {
		features = append(features, "long-thread")
	}
	return features
}

func hasRequiredToolChoice(rawJSON []byte) bool {
	toolChoice := gjson.GetBytes(rawJSON, "tool_choice")
	if !toolChoice.Exists() {
		return false
	}
	return toolChoice.Type == gjson.String && strings.EqualFold(strings.TrimSpace(toolChoice.String()), "required")
}

func structuredMediaFeatures(rawJSON []byte) (hasImage, hasFile, hasVideo bool) {
	if len(rawJSON) == 0 {
		return false, false, false
	}
	var payload any
	if err := json.Unmarshal(rawJSON, &payload); err != nil {
		return false, false, false
	}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "type":
					if valueType, ok := child.(string); ok {
						switch strings.ToLower(strings.TrimSpace(valueType)) {
						case "input_image", "image":
							hasImage = true
						case "input_file", "file":
							hasFile = true
						case "input_video", "video":
							hasVideo = true
						}
					}
				case "image_url":
					hasImage = true
				case "file_url":
					hasFile = true
				case "video_url":
					hasVideo = true
				}
				walk(child)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(payload)
	return hasImage, hasFile, hasVideo
}

func hasMCPTool(rawJSON []byte) bool {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		if strings.EqualFold(strings.TrimSpace(tool.Get("type").String()), "mcp") || tool.Get("mcp").Exists() {
			found = true
			return false
		}
		return true
	})
	return found
}

func hasXunfeiUnsupportedTool(rawJSON []byte) bool {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return false
	}
	unsupported := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
		if toolType == "" || toolType == "function" || toolType == "web_search" || strings.HasPrefix(toolType, "web_search_") {
			return true
		}
		unsupported = true
		return false
	})
	return unsupported
}
