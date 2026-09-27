package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func sanitizeOpenAIResponsesReasoningItems(ctx context.Context, provider string, body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}
	provider = openAIResponsesSignatureProviderName(provider)
	items := input.Array()
	replayableItems := make([]string, 0, len(items))
	dropped := false
	for index, item := range items {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			replayableItems = append(replayableItems, item.Raw)
			continue
		}
		encryptedContent := item.Get("encrypted_content")
		if !encryptedContent.Exists() {
			replayableItems = append(replayableItems, item.Raw)
			continue
		}
		reason := invalidGPTReasoningEncryptedContentReason(encryptedContent)
		if reason == "" {
			replayableItems = append(replayableItems, item.Raw)
			continue
		}
		dropped = true
		helps.LogWithRequestID(ctx).Debugf("%s: dropped unreplayable reasoning item at input[%d] reason=%s", provider, index, reason)
	}
	updated := body
	if dropped {
		var err error
		updated, err = sjson.SetRawBytes(body, "input", []byte("["+strings.Join(replayableItems, ",")+"]"))
		if err != nil {
			return body
		}
	}
	return sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, provider, updated)
}

func invalidGPTReasoningEncryptedContentReason(encryptedContent gjson.Result) string {
	switch encryptedContent.Type {
	case gjson.String:
		rawSignature := encryptedContent.String()
		if rawSignature != strings.TrimSpace(rawSignature) {
			return "encrypted_content has leading or trailing whitespace"
		}
		if _, err := signature.InspectGPTReasoningSignature(rawSignature); err != nil {
			return err.Error()
		}
		return ""
	case gjson.Null:
		return "encrypted_content is null"
	default:
		return fmt.Sprintf("encrypted_content must be a string, got %s", encryptedContent.Type.String())
	}
}

func dropOpenAIResponsesReasoningItemsWithEncryptedContent(ctx context.Context, provider string, body []byte, reason string) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body, false
	}
	provider = openAIResponsesSignatureProviderName(provider)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "upstream rejected reasoning encrypted_content"
	}
	items := input.Array()
	remainingItems := make([]string, 0, len(items))
	dropped := false
	for index, item := range items {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" || !item.Get("encrypted_content").Exists() {
			remainingItems = append(remainingItems, item.Raw)
			continue
		}
		dropped = true
		helps.LogWithRequestID(ctx).Debugf("%s: dropped reasoning item at input[%d] reason=%s", provider, index, reason)
	}
	if !dropped {
		return body, false
	}
	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(remainingItems, ",")+"]"))
	if err != nil {
		return body, false
	}
	return updated, true
}

func openAIResponsesSignatureProviderName(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "openai responses upstream"
	}
	return provider
}
