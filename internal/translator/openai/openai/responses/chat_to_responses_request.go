package responses

import (
	"strings"

	codexchat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIChatCompletionsRequestToOpenAIResponses(modelName string, inputRawJSON []byte, stream bool) []byte {
	out := codexchat.ConvertOpenAIRequestToCodex(modelName, inputRawJSON, stream)
	root := gjson.ParseBytes(inputRawJSON)

	out, _ = sjson.DeleteBytes(out, "include")
	if instructions := gjson.GetBytes(out, "instructions"); instructions.Exists() && strings.TrimSpace(instructions.String()) == "" {
		out, _ = sjson.DeleteBytes(out, "instructions")
	}
	if effort := root.Get("reasoning_effort"); effort.Exists() {
		out, _ = sjson.SetBytes(out, "reasoning.effort", effort.String())
		out, _ = sjson.DeleteBytes(out, "reasoning.summary")
	} else {
		out, _ = sjson.DeleteBytes(out, "reasoning")
	}
	if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Exists() {
		out, _ = sjson.SetBytes(out, "parallel_tool_calls", parallelToolCalls.Bool())
	} else {
		out, _ = sjson.DeleteBytes(out, "parallel_tool_calls")
	}
	if store := root.Get("store"); store.Exists() {
		out, _ = sjson.SetBytes(out, "store", store.Bool())
	} else {
		out, _ = sjson.DeleteBytes(out, "store")
	}

	if maxTokens := root.Get("max_completion_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_output_tokens", maxTokens.Int())
	} else if maxTokens = root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_output_tokens", maxTokens.Int())
	}

	for _, field := range []string{
		"temperature",
		"top_p",
		"metadata",
		"service_tier",
		"user",
		"truncation",
		"previous_response_id",
		"prompt_cache_key",
		"safety_identifier",
	} {
		if value := root.Get(field); value.Exists() {
			out, _ = sjson.SetRawBytes(out, field, []byte(value.Raw))
		}
	}

	out, _ = sjson.SetBytes(out, "model", modelName)
	out, _ = sjson.SetBytes(out, "stream", stream)
	return out
}
