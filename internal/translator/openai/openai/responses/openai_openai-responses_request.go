package responses

import (
	"strings"

	codexchat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIChatCompletionsRequestToOpenAIResponses converts a standard
// Chat Completions request into the OpenAI Responses request schema.
//
// The Codex translator already owns the structural messages/tools conversion.
// This wrapper removes Codex-only defaults and restores the generation fields
// that generic OpenAI-compatible Responses providers accept.
func ConvertOpenAIChatCompletionsRequestToOpenAIResponses(modelName string, inputRawJSON []byte, stream bool) []byte {
	out := codexchat.ConvertOpenAIRequestToCodex(modelName, inputRawJSON, stream)
	root := gjson.ParseBytes(inputRawJSON)

	// Codex-specific defaults must not leak into generic Responses providers.
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

// ConvertOpenAIResponsesRequestToOpenAIChatCompletions converts OpenAI responses format to OpenAI chat completions format.
// It transforms the OpenAI responses API format (with instructions and input array) into the standard
// OpenAI chat completions format (with messages array and system content).
//
// The conversion handles:
// 1. Model name and streaming configuration
// 2. Instructions to system message conversion
// 3. Input array to messages array transformation
// 4. Tool definitions and tool choice conversion
// 5. Function calls and function results handling
// 6. Generation parameters mapping (max_tokens, reasoning, etc.)
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data in OpenAI responses format
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in OpenAI chat completions format
func ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	// Base OpenAI chat completions template with default values
	out := []byte(`{"model":"","messages":[],"stream":false}`)

	root := gjson.ParseBytes(rawJSON)

	// Set model name
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Set stream configuration
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Map generation parameters from responses format to chat completions format
	if maxTokens := root.Get("max_output_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Exists() {
		out, _ = sjson.SetBytes(out, "parallel_tool_calls", parallelToolCalls.Bool())
	}

	// Convert instructions to system message
	if instructions := root.Get("instructions"); instructions.Exists() {
		systemMessage := []byte(`{"role":"system","content":""}`)
		systemMessage, _ = sjson.SetBytes(systemMessage, "content", instructions.String())
		out, _ = sjson.SetRawBytes(out, "messages.-1", systemMessage)
	}

	// Convert input array to messages
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		inputItems := input.Array()
		outputCallIDs := make(map[string]struct{})
		preScanPendingCallIDs := make([]string, 0)
		removePreScanPendingCallID := func(callID string) {
			for i, pendingID := range preScanPendingCallIDs {
				if pendingID == callID {
					preScanPendingCallIDs = append(preScanPendingCallIDs[:i], preScanPendingCallIDs[i+1:]...)
					return
				}
			}
		}
		for _, item := range inputItems {
			itemType := item.Get("type").String()
			callID := ""
			switch itemType {
			case "function_call", "custom_tool_call":
				preScanPendingCallIDs = append(preScanPendingCallIDs, strings.TrimSpace(item.Get("call_id").String()))
				continue
			case "function_call_output", "custom_tool_call_output":
				callID = strings.TrimSpace(item.Get("call_id").String())
			case "message", "":
				if strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "tool") {
					callID = strings.TrimSpace(item.Get("tool_call_id").String())
					if callID == "" {
						callID = strings.TrimSpace(item.Get("call_id").String())
					}
					if callID == "" && len(preScanPendingCallIDs) == 1 {
						callID = preScanPendingCallIDs[0]
					}
				}
			}
			if callID == "" {
				continue
			}
			outputCallIDs[callID] = struct{}{}
			removePreScanPendingCallID(callID)
		}

		pendingToolCalls := make([]interface{}, 0)
		pendingToolCallIDs := make([]string, 0)
		pendingReasoningContent := ""
		awaitingToolOutputs := make(map[string]struct{})
		discardedToolCallIDs := make(map[string]struct{})
		deferredMessages := make([][]byte, 0)

		takePendingReasoningContent := func() string {
			reasoningContent := pendingReasoningContent
			pendingReasoningContent = ""
			return reasoningContent
		}
		flushPendingToolCalls := func() {
			if len(pendingToolCalls) == 0 {
				return
			}
			assistantMessage := []byte(`{"role":"assistant","tool_calls":[]}`)
			assistantMessage, _ = sjson.SetBytes(assistantMessage, "tool_calls", pendingToolCalls)
			if reasoningContent := takePendingReasoningContent(); reasoningContent != "" {
				assistantMessage, _ = sjson.SetBytes(assistantMessage, "reasoning_content", reasoningContent)
			}
			out, _ = sjson.SetRawBytes(out, "messages.-1", assistantMessage)
			for _, id := range pendingToolCallIDs {
				if strings.TrimSpace(id) == "" {
					continue
				}
				awaitingToolOutputs[id] = struct{}{}
			}
			pendingToolCalls = pendingToolCalls[:0]
			pendingToolCallIDs = pendingToolCallIDs[:0]
		}
		flushDeferredMessages := func() {
			for _, message := range deferredMessages {
				out, _ = sjson.SetRawBytes(out, "messages.-1", message)
			}
			deferredMessages = deferredMessages[:0]
		}
		hasAwaitingToolOutput := func() bool {
			for id := range awaitingToolOutputs {
				if _, ok := outputCallIDs[id]; ok {
					return true
				}
			}
			return false
		}
		appendRegularMessage := func(message []byte) {
			// Keep tool-call adjacency strict for providers that require
			// assistant(tool_calls) -> tool(tool_call_id) with no message in between.
			if hasAwaitingToolOutput() {
				deferredMessages = append(deferredMessages, message)
				return
			}
			out, _ = sjson.SetRawBytes(out, "messages.-1", message)
		}
		appendPendingReasoningMessage := func() {
			reasoningContent := takePendingReasoningContent()
			if reasoningContent == "" {
				return
			}
			message := []byte(`{"role":"assistant","content":"","reasoning_content":""}`)
			message, _ = sjson.SetBytes(message, "reasoning_content", reasoningContent)
			appendRegularMessage(message)
		}

		for _, item := range inputItems {
			itemType := item.Get("type").String()
			if itemType == "" && item.Get("role").String() != "" {
				itemType = "message"
			}
			messageToolCallID := ""
			if itemType == "message" && strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "tool") {
				messageToolCallID = strings.TrimSpace(item.Get("tool_call_id").String())
				if messageToolCallID == "" {
					messageToolCallID = strings.TrimSpace(item.Get("call_id").String())
				}
				if _, discarded := discardedToolCallIDs[messageToolCallID]; messageToolCallID != "" && discarded {
					continue
				}
				if messageToolCallID == "" && len(awaitingToolOutputs) == 0 && len(pendingToolCalls) == 1 && len(pendingToolCallIDs) == 1 {
					messageToolCallID = pendingToolCallIDs[0]
				}
				if messageToolCallID == "" && len(pendingToolCalls) == 0 && len(awaitingToolOutputs) == 1 {
					for callID := range awaitingToolOutputs {
						messageToolCallID = callID
					}
				}
				if messageToolCallID == "" {
					// An ambiguous result cannot safely link to a parallel call. Drop
					// the pending calls as well so no orphan reaches the upstream.
					for _, callID := range pendingToolCallIDs {
						if callID != "" {
							discardedToolCallIDs[callID] = struct{}{}
						}
					}
					pendingToolCalls = pendingToolCalls[:0]
					pendingToolCallIDs = pendingToolCallIDs[:0]
					continue
				}
			}
			if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
				callID := strings.TrimSpace(item.Get("call_id").String())
				if _, discarded := discardedToolCallIDs[callID]; callID != "" && discarded {
					continue
				}
			}
			if itemType != "function_call" && itemType != "custom_tool_call" {
				flushPendingToolCalls()
			}

			switch itemType {
			case "message", "":
				// Handle regular message conversion
				role := item.Get("role").String()
				if role == "developer" {
					role = "user"
				}
				if role != "assistant" {
					appendPendingReasoningMessage()
				}
				message := []byte(`{"role":"","content":[]}`)
				message, _ = sjson.SetBytes(message, "role", role)
				if messageToolCallID != "" {
					message, _ = sjson.SetBytes(message, "tool_call_id", messageToolCallID)
				}

				if content := item.Get("content"); content.Exists() && content.IsArray() {
					var messageContent string
					var toolCalls []interface{}

					content.ForEach(func(_, contentItem gjson.Result) bool {
						contentType := contentItem.Get("type").String()
						if contentType == "" {
							contentType = "input_text"
						}

						switch contentType {
						case "input_text", "output_text":
							text := contentItem.Get("text").String()
							contentPart := []byte(`{"type":"text","text":""}`)
							contentPart, _ = sjson.SetBytes(contentPart, "text", text)
							message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
						case "input_image":
							imageURL := contentItem.Get("image_url").String()
							contentPart := []byte(`{"type":"image_url","image_url":{"url":""}}`)
							contentPart, _ = sjson.SetBytes(contentPart, "image_url.url", imageURL)
							if detail := contentItem.Get("detail"); detail.Exists() {
								contentPart, _ = sjson.SetBytes(contentPart, "image_url.detail", detail.String())
							}
							message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
						}
						return true
					})

					if messageContent != "" {
						message, _ = sjson.SetBytes(message, "content", messageContent)
					}

					if len(toolCalls) > 0 {
						message, _ = sjson.SetBytes(message, "tool_calls", toolCalls)
					}
				} else if content.Type == gjson.String {
					message, _ = sjson.SetBytes(message, "content", content.String())
				}

				if role == "assistant" {
					reasoningContent := item.Get("reasoning_content").String()
					if reasoningContent == "" {
						reasoningContent = takePendingReasoningContent()
					} else {
						pendingReasoningContent = ""
					}
					if reasoningContent != "" {
						message, _ = sjson.SetBytes(message, "reasoning_content", reasoningContent)
					}
				}

				if role == "tool" {
					out, _ = sjson.SetRawBytes(out, "messages.-1", message)
					delete(awaitingToolOutputs, messageToolCallID)
					if len(awaitingToolOutputs) == 0 && len(deferredMessages) > 0 {
						flushDeferredMessages()
					}
				} else {
					appendRegularMessage(message)
				}

			case "reasoning":
				reasoningContent := collectOpenAIResponsesReasoningContent(item)
				if pendingReasoningContent == "" {
					pendingReasoningContent = reasoningContent
				} else {
					pendingReasoningContent += reasoningContent
				}

			case "function_call":
				// Buffer consecutive function calls and emit them as one assistant message.
				toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)

				if callId := item.Get("call_id"); callId.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "id", callId.String())
				}

				if name := item.Get("name"); name.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "function.name", name.String())
				}

				if arguments := item.Get("arguments"); arguments.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", arguments.String())
				}
				pendingToolCalls = append(pendingToolCalls, gjson.ParseBytes(toolCall).Value())
				if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
					pendingToolCallIDs = append(pendingToolCallIDs, callID)
				}

			case "function_call_output":
				// Handle function call output conversion to tool message
				toolMessage := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
				callID := ""

				if callId := item.Get("call_id"); callId.Exists() {
					callID = strings.TrimSpace(callId.String())
					toolMessage, _ = sjson.SetBytes(toolMessage, "tool_call_id", callID)
				}

				if output := item.Get("output"); output.Exists() {
					toolMessage, _ = sjson.SetBytes(toolMessage, "content", output.String())
				}

				out, _ = sjson.SetRawBytes(out, "messages.-1", toolMessage)
				if callID != "" {
					delete(awaitingToolOutputs, callID)
				}
				if len(awaitingToolOutputs) == 0 && len(deferredMessages) > 0 {
					flushDeferredMessages()
				}

			case "custom_tool_call":
				// Codex freeform tool call replay: wrap the raw input so it
				// matches the {"input": string} function shape used when
				// converting custom tool definitions.
				toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
				toolCall, _ = sjson.SetBytes(toolCall, "id", item.Get("call_id").String())
				toolCall, _ = sjson.SetBytes(toolCall, "function.name", item.Get("name").String())
				wrappedArgs, _ := sjson.SetBytes([]byte(`{"input":""}`), "input", item.Get("input").String())
				toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", string(wrappedArgs))
				pendingToolCalls = append(pendingToolCalls, gjson.ParseBytes(toolCall).Value())
				if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
					pendingToolCallIDs = append(pendingToolCallIDs, callID)
				}

			case "custom_tool_call_output":
				toolMessage := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
				callID := strings.TrimSpace(item.Get("call_id").String())
				toolMessage, _ = sjson.SetBytes(toolMessage, "tool_call_id", callID)
				toolMessage, _ = sjson.SetBytes(toolMessage, "content", responsesToolOutputText(item.Get("output")))
				out, _ = sjson.SetRawBytes(out, "messages.-1", toolMessage)
				if callID != "" {
					delete(awaitingToolOutputs, callID)
				}
				if len(awaitingToolOutputs) == 0 && len(deferredMessages) > 0 {
					flushDeferredMessages()
				}
			}

		}
		flushPendingToolCalls()
		appendPendingReasoningMessage()
		flushDeferredMessages()
	} else if input.Type == gjson.String {
		msg := []byte(`{}`)
		msg, _ = sjson.SetBytes(msg, "role", "user")
		msg, _ = sjson.SetBytes(msg, "content", input.String())
		out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	}

	// Convert tools from responses format to chat completions format.
	// Codex Desktop (Responses Lite) delivers tool definitions through an
	// "additional_tools" input item instead of the top-level "tools" field,
	// so merge both sources.
	var chatCompletionsTools []interface{}
	appendChatTools := func(tools gjson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			for _, chatTool := range convertResponsesToolToOpenAIChatTools(tool) {
				chatCompletionsTools = append(chatCompletionsTools, gjson.ParseBytes(chatTool).Value())
			}
			return true
		})
	}
	appendChatTools(root.Get("tools"))
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				appendChatTools(item.Get("tools"))
			}
			return true
		})
	}
	if len(chatCompletionsTools) > 0 {
		out, _ = sjson.SetBytes(out, "tools", chatCompletionsTools)
	}

	if reasoningEffort := root.Get("reasoning.effort"); reasoningEffort.Exists() {
		effort := strings.ToLower(strings.TrimSpace(reasoningEffort.String()))
		if effort != "" {
			out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
		}
	}

	// Convert tool_choice if present
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(toolChoice.Raw))
	}

	return out
}

func collectOpenAIResponsesReasoningContent(item gjson.Result) string {
	var reasoningText strings.Builder
	if summary := item.Get("summary"); summary.Exists() && summary.IsArray() {
		summary.ForEach(func(_, summaryItem gjson.Result) bool {
			if summaryItem.Get("type").String() != "summary_text" {
				return true
			}
			reasoningText.WriteString(summaryItem.Get("text").String())
			return true
		})
	}
	if reasoningText.Len() == 0 {
		return "[reasoning unavailable]"
	}
	return reasoningText.String()
}
