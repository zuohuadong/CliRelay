package executor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const astronCompactSystemPrompt = "You compact coding-agent conversation history. Produce a concise continuation summary that preserves the user's latest intent, completed work, open issues, exact commands or IDs that matter, and safe next steps. Do not invent facts."

func buildAstronCompactChatPayload(rawJSON []byte, baseModel string) ([]byte, error) {
	transcript := compactTranscriptForPrompt(rawJSON)
	body := map[string]any{
		"model":       baseModel,
		"stream":      false,
		"temperature": 0,
		"max_tokens":  2048,
		"messages": []map[string]string{
			{"role": "system", "content": astronCompactSystemPrompt},
			{"role": "user", "content": "Compact this conversation transcript for continuation. Return only the summary text.\n\n" + transcript},
		},
	}
	return json.Marshal(body)
}

func compactTranscriptForPrompt(rawJSON []byte) string {
	if input := gjson.GetBytes(rawJSON, "input"); input.Exists() {
		return input.Raw
	}
	if messages := gjson.GetBytes(rawJSON, "messages"); messages.Exists() {
		return messages.Raw
	}
	return string(rawJSON)
}

func buildAstronCompactResponse(originalRequest []byte, model string, chatResponse []byte, upstreamHeaders http.Header) (cliproxyexecutor.Response, error) {
	summary := strings.TrimSpace(gjson.GetBytes(chatResponse, "choices.0.message.content").String())
	if summary == "" {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: "astron compact: upstream chat response did not include summary content"}
	}

	idSuffix := time.Now().UnixNano()
	output := make([]any, 0, 8)
	for _, item := range astronCompactUserItems(originalRequest) {
		output = append(output, item)
	}
	output = append(output, map[string]any{
		"id":                fmt.Sprintf("cmpct_%d", idSuffix),
		"type":              "compaction",
		"encrypted_content": summary,
		"created_by":        "astron-code",
	})

	inputTokens := compactUsageToken(chatResponse, "usage.input_tokens", "usage.prompt_tokens")
	outputTokens := compactUsageToken(chatResponse, "usage.output_tokens", "usage.completion_tokens")
	totalTokens := compactUsageToken(chatResponse, "usage.total_tokens")
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens
	}
	wrapped := map[string]any{
		"id":         fmt.Sprintf("resp_compact_%d", idSuffix),
		"created_at": time.Now().Unix(),
		"object":     "response.compaction",
		"model":      model,
		"output":     output,
		"usage": map[string]any{
			"input_tokens":          inputTokens,
			"output_tokens":         outputTokens,
			"total_tokens":          totalTokens,
			"input_tokens_details":  map[string]any{"cached_tokens": 0},
			"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		},
	}
	payload, err := json.Marshal(wrapped)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: payload, Headers: upstreamHeaders.Clone()}, nil
}

func compactUsageToken(rawJSON []byte, paths ...string) int64 {
	for _, path := range paths {
		if value := gjson.GetBytes(rawJSON, path); value.Exists() {
			return value.Int()
		}
	}
	return 0
}

func astronCompactUserItems(rawJSON []byte) []map[string]any {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.IsArray() {
		return nil
	}
	items := make([]map[string]any, 0)
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("role").String()) != "user" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(item.Raw), &decoded); err != nil {
			continue
		}
		items = append(items, decoded)
	}
	return items
}
