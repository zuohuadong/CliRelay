package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func openAICompatForwardSemanticStreamChunks(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, pending *[][]byte, semanticOutput *bool, chunks [][]byte) bool {
	if len(chunks) == 0 {
		return true
	}
	if semanticOutput != nil && !*semanticOutput {
		if openAICompatStreamChunksHaveSemanticOutput(chunks) {
			*semanticOutput = true
			if pending != nil && len(*pending) > 0 {
				if !openAICompatSendStreamChunks(ctx, out, *pending) {
					return false
				}
				*pending = nil
			}
		} else {
			if pending != nil {
				*pending = append(*pending, chunks...)
			}
			return true
		}
	}
	return openAICompatSendStreamChunks(ctx, out, chunks)
}

func openAICompatSendStreamChunks(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, chunks [][]byte) bool {
	for i := range chunks {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

func openAICompatStreamChunksHaveSemanticOutput(chunks [][]byte) bool {
	for _, chunk := range chunks {
		if openAICompatStreamChunkHasSemanticOutput(chunk) {
			return true
		}
	}
	return false
}

func openAICompatStreamChunkHasSemanticOutput(chunk []byte) bool {
	for _, payload := range openAICompatSSEDataPayloads(chunk) {
		if openAICompatStreamPayloadHasSemanticOutput(payload) {
			return true
		}
	}
	return false
}

func openAICompatSSEDataPayloads(chunk []byte) [][]byte {
	chunk = bytes.TrimSpace(chunk)
	if len(chunk) == 0 {
		return nil
	}
	var payloads [][]byte
	frames := bytes.Split(chunk, []byte("\n\n"))
	for _, frame := range frames {
		var payload bytes.Buffer
		for _, line := range bytes.Split(frame, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			data := bytes.TrimSpace(line[len("data:"):])
			if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
				continue
			}
			if payload.Len() > 0 {
				payload.WriteByte('\n')
			}
			payload.Write(data)
		}
		if payload.Len() > 0 {
			payloads = append(payloads, append([]byte(nil), payload.Bytes()...))
		}
	}
	if len(payloads) == 0 && json.Valid(chunk) {
		payloads = append(payloads, append([]byte(nil), chunk...))
	}
	return payloads
}

func openAICompatStreamPayloadHasSemanticOutput(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return false
	}
	payloadType := gjson.GetBytes(payload, "type").String()
	if strings.HasPrefix(payloadType, "response.image_generation_call.") ||
		strings.HasPrefix(payloadType, "image_generation.") {
		return true
	}
	switch payloadType {
	case "response.output_item.added",
		"response.output_item.done",
		"response.content_part.added",
		"response.content_part.done",
		"response.output_text.delta",
		"response.output_text.done",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done":
		return true
	case "response.completed":
		output := gjson.GetBytes(payload, "response.output")
		return output.IsArray() && len(output.Array()) > 0
	}
	return openAICompatChatCompletionPayloadHasSemanticOutput(payload)
}

func openAICompatChatCompletionPayloadHasSemanticOutput(payload []byte) bool {
	choices := gjson.GetBytes(payload, "choices")
	if !choices.IsArray() {
		return false
	}
	semantic := false
	choices.ForEach(func(_, choice gjson.Result) bool {
		if strings.TrimSpace(choice.Get("text").String()) != "" {
			semantic = true
			return false
		}
		message := choice.Get("message")
		if strings.TrimSpace(message.Get("content").String()) != "" {
			semantic = true
			return false
		}
		delta := choice.Get("delta")
		if strings.TrimSpace(delta.Get("content").String()) != "" ||
			strings.TrimSpace(delta.Get("reasoning_content").String()) != "" {
			semantic = true
			return false
		}
		if functionCall := delta.Get("function_call"); functionCall.Exists() {
			if strings.TrimSpace(functionCall.Get("name").String()) != "" ||
				strings.TrimSpace(functionCall.Get("arguments").String()) != "" {
				semantic = true
				return false
			}
		}
		if toolCalls := delta.Get("tool_calls"); toolCalls.IsArray() {
			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				if strings.TrimSpace(toolCall.Get("id").String()) != "" ||
					strings.TrimSpace(toolCall.Get("function.name").String()) != "" ||
					strings.TrimSpace(toolCall.Get("function.arguments").String()) != "" {
					semantic = true
					return false
				}
				return true
			})
			if semantic {
				return false
			}
		}
		return true
	})
	return semantic
}
