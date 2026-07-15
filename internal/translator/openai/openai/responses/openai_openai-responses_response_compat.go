package responses

import (
	"context"

	codexchat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIResponsesResponseToOpenAIChatCompletions converts Responses SSE
// events into Chat Completions streaming chunks. Responses and Codex use the
// same event schema, so reuse the established Responses-event converter.
func ConvertOpenAIResponsesResponseToOpenAIChatCompletions(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	return codexchat.ConvertCodexResponseToOpenAI(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// ConvertOpenAIResponsesResponseToOpenAIChatCompletionsNonStream converts a
// complete Responses object into a Chat Completions response. Codex's
// non-stream converter consumes response.completed envelopes, while standard
// Responses endpoints return the response object directly, so wrap it first.
func ConvertOpenAIResponsesResponseToOpenAIChatCompletionsNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	root := gjson.ParseBytes(rawJSON)
	if root.Get("type").String() == "" && root.Get("object").String() == "response" {
		wrapped := []byte(`{"type":"response.completed","response":{}}`)
		wrapped, _ = sjson.SetRawBytes(wrapped, "response", rawJSON)
		rawJSON = wrapped
	}
	return codexchat.ConvertCodexResponseToOpenAINonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}
