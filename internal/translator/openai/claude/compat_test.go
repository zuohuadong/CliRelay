package claude

import (
	"encoding/json"

	"github.com/MoeForge/moego/ojson"
	"github.com/tidwall/gjson"
)

type streamToolDeltaSpec struct {
	Index   int
	ID      string
	Name    string
	Args    string
	HasName bool
	HasArgs bool
}

func openAIStreamChunk(id string, deltas []streamToolDeltaSpec, finish string) string {
	choice := map[string]any{"index": 0, "delta": map[string]any{}}
	delta := choice["delta"].(map[string]any)
	if len(deltas) > 0 {
		calls := make([]any, 0, len(deltas))
		for _, item := range deltas {
			call := map[string]any{"index": item.Index, "type": "function", "function": map[string]any{}}
			if item.ID != "" {
				call["id"] = item.ID
			}
			fn := call["function"].(map[string]any)
			if item.HasName {
				fn["name"] = item.Name
			}
			if item.HasArgs {
				fn["arguments"] = item.Args
			}
			calls = append(calls, call)
		}
		delta["tool_calls"] = calls
	}
	choice["finish_reason"] = nil
	if finish != "" {
		choice["finish_reason"] = finish
	}
	body := map[string]any{"id": id, "object": "chat.completion.chunk", "created": 1, "model": "m", "choices": []any{choice}}
	raw, _ := json.Marshal(body)
	return "data: " + string(raw)
}

func openAIStreamUsageChunk(id string) string {
	body := map[string]any{"id": id, "object": "chat.completion.chunk", "created": 1, "model": "m", "choices": []any{}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}
	raw, _ := json.Marshal(body)
	return "data: " + string(raw)
}

func convertClaudeToolResultContentToString(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	return content.Raw
}

func convertClaudeContentPartCompat(part gjson.Result) (string, bool) {
	return convertClaudeContentPart(ojson.Parse(part.Raw))
}
