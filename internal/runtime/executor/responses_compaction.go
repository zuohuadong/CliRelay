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

const responsesCompactSystemPrompt = "You compact coding-agent conversation history. Produce a concise continuation summary that preserves the user's latest intent, completed work, open issues, exact commands or IDs that matter, and safe next steps. Do not invent facts."

func buildResponsesCompactChatPayload(rawJSON []byte, baseModel string) ([]byte, error) {
	transcript := compactTranscriptForPrompt(rawJSON)
	body := map[string]any{
		"model":       baseModel,
		"stream":      false,
		"temperature": 0,
		"max_tokens":  2048,
		"messages": []map[string]string{
			{"role": "system", "content": responsesCompactSystemPrompt},
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

func buildResponsesCompactResponse(model string, chatResponse []byte, upstreamHeaders http.Header) (cliproxyexecutor.Response, error) {
	summary := strings.TrimSpace(gjson.GetBytes(chatResponse, "choices.0.message.content").String())
	if summary == "" {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: "responses compact: upstream chat response did not include summary content"}
	}

	idSuffix := time.Now().UnixNano()
	output := []map[string]any{
		{
			"id":                fmt.Sprintf("cmpct_%d", idSuffix),
			"type":              "compaction",
			"encrypted_content": summary,
		},
	}

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

func buildResponsesCompactionStreamChunks(compactPayload []byte, model string) [][]byte {
	responseID := strings.TrimSpace(gjson.GetBytes(compactPayload, "id").String())
	if responseID == "" {
		responseID = fmt.Sprintf("resp_compact_%d", time.Now().UnixNano())
	}
	createdAt := gjson.GetBytes(compactPayload, "created_at").Int()
	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}
	compactionItem := responsesCompactionOutputItem(compactPayload, responseID)
	output := []json.RawMessage{compactionItem}
	metadata := responsesCompactionStreamMetadata{responseID: responseID, model: model, createdAt: createdAt}

	createdResponse := responsesCompactionStreamResponse(compactPayload, metadata, "in_progress", nil)
	completedResponse := responsesCompactionStreamResponse(compactPayload, metadata, "completed", output)

	return [][]byte{
		responsesSSEFrame("response.created", map[string]any{
			"type":            "response.created",
			"sequence_number": 0,
			"response":        createdResponse,
		}),
		responsesSSEFrame("response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": 1,
			"output_index":    0,
			"item":            compactionItem,
		}),
		responsesSSEFrame("response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": 2,
			"output_index":    0,
			"item":            compactionItem,
		}),
		responsesSSEFrame("response.completed", map[string]any{
			"type":            "response.completed",
			"sequence_number": 3,
			"response":        completedResponse,
		}),
	}
}

type responsesCompactionStreamMetadata struct {
	responseID string
	model      string
	createdAt  int64
}

func responsesCompactionStreamResult(resp cliproxyexecutor.Response, model string) *cliproxyexecutor.StreamResult {
	headers := resp.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")
	chunks := buildResponsesCompactionStreamChunks(resp.Payload, model)
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(out)
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
}

func responsesCompactionStreamResponse(compactPayload []byte, metadata responsesCompactionStreamMetadata, status string, output []json.RawMessage) map[string]any {
	if output == nil {
		output = []json.RawMessage{}
	}
	responseModel := strings.TrimSpace(gjson.GetBytes(compactPayload, "model").String())
	if responseModel == "" {
		responseModel = metadata.model
	}
	response := map[string]any{
		"id":                 metadata.responseID,
		"object":             "response",
		"created_at":         metadata.createdAt,
		"status":             status,
		"model":              responseModel,
		"background":         false,
		"error":              nil,
		"incomplete_details": nil,
		"output":             output,
	}
	if usage := gjson.GetBytes(compactPayload, "usage"); usage.Exists() && usage.Type == gjson.JSON {
		response["usage"] = json.RawMessage(usage.Raw)
	}
	return response
}

func responsesCompactionOutputItem(compactPayload []byte, responseID string) json.RawMessage {
	compactionItem := map[string]any{
		"id":                fmt.Sprintf("cmpct_%d", time.Now().UnixNano()),
		"type":              "compaction",
		"encrypted_content": strings.TrimSpace(gjson.GetBytes(compactPayload, "output.0.encrypted_content").String()),
	}
	if outputItem := gjson.GetBytes(compactPayload, "output.0"); outputItem.Exists() && outputItem.Type == gjson.JSON {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(outputItem.Raw), &parsed); err == nil && parsed != nil {
			compactionItem = parsed
		}
	}
	if id, ok := compactionItem["id"].(string); !ok || strings.TrimSpace(id) == "" {
		compactionItem["id"] = fmt.Sprintf("cmpct_%s", responseID)
	}
	if itemType, ok := compactionItem["type"].(string); !ok || strings.TrimSpace(itemType) == "" {
		compactionItem["type"] = "compaction"
	}
	raw, _ := json.Marshal(compactionItem)
	return json.RawMessage(raw)
}

func responsesSSEFrame(event string, payload any) []byte {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{"type":"error"}`)
	}
	return []byte("event: " + event + "\ndata: " + string(raw) + "\n\n")
}
