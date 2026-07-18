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
		itemID := strings.TrimSpace(item.Get("id").String())
		if itemID == "" {
			itemID = fmt.Sprintf("input[%d]", index)
		}
		helps.LogWithRequestID(ctx).Debugf("%s: dropped unreplayable reasoning item at input[%d] item_id=%q reason=%s", provider, index, itemID, reason)
	}

	updated := body
	if dropped {
		var err error
		updated, err = sjson.SetRawBytes(body, "input", []byte("["+strings.Join(replayableItems, ",")+"]"))
		if err != nil {
			helps.LogWithRequestID(ctx).Debugf("%s: failed to replace input after dropping unreplayable reasoning items: %v", provider, err)
			return body
		}
	}

	return sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, provider, updated)
}

// sanitizeOpenAIResponsesReasoningEncryptedContent 保留上游的字段级清理语义。
// Codex 运行路径会先用上面的严格策略删除不可重放项，再由这里清除孤立 ID。
func sanitizeOpenAIResponsesReasoningEncryptedContent(ctx context.Context, provider string, body []byte) []byte {
	inputResult := gjson.GetBytes(body, "input")
	if !inputResult.Exists() || !inputResult.IsArray() {
		return body
	}
	provider = openAIResponsesSignatureProviderName(provider)
	stripOrphanReasoningIDs := !gjson.GetBytes(body, "store").Bool()
	items := inputResult.Array()

	var rebuilt []byte
	itemsWritten := 0
	keep := func(raw string) {
		if rebuilt == nil {
			return
		}
		if itemsWritten > 0 {
			rebuilt = append(rebuilt, ',')
		}
		rebuilt = append(rebuilt, raw...)
		itemsWritten++
	}
	startRebuild := func(index int) {
		if rebuilt != nil {
			return
		}
		rebuilt = make([]byte, 0, len(inputResult.Raw))
		rebuilt = append(rebuilt, '[')
		for i := range index {
			keep(items[i].Raw)
		}
	}

	for index, item := range items {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			keep(item.Raw)
			continue
		}

		encryptedContent := item.Get("encrypted_content")
		itemID := strings.TrimSpace(item.Get("id").String())
		if itemID == "" {
			itemID = fmt.Sprintf("input[%d]", index)
		}
		if !encryptedContent.Exists() {
			if stripOrphanReasoningIDs && item.Get("id").Exists() {
				nextItem, err := sjson.Delete(item.Raw, "id")
				if err != nil {
					helps.LogWithRequestID(ctx).Debugf("%s: failed to drop orphan reasoning id at input[%d]: %v", provider, index, err)
					keep(item.Raw)
					continue
				}
				startRebuild(index)
				keep(nextItem)
				helps.LogWithRequestID(ctx).Debugf("%s: dropped orphan reasoning id at input[%d] item_id=%q", provider, index, itemID)
				continue
			}
			keep(item.Raw)
			continue
		}

		reason := invalidGPTReasoningEncryptedContentReason(encryptedContent)
		if reason == "" {
			keep(item.Raw)
			continue
		}

		nextItem, err := sjson.Delete(item.Raw, "encrypted_content")
		if err != nil {
			helps.LogWithRequestID(ctx).Debugf("%s: failed to drop invalid reasoning encrypted_content at input[%d]: %v", provider, index, err)
			keep(item.Raw)
			continue
		}
		if stripOrphanReasoningIDs && item.Get("id").Exists() {
			if nextID, errID := sjson.Delete(nextItem, "id"); errID == nil {
				nextItem = nextID
			}
		}
		startRebuild(index)
		keep(nextItem)
		helps.LogWithRequestID(ctx).Debugf("%s: dropped invalid reasoning encrypted_content at input[%d] item_id=%q reason=%s", provider, index, itemID, reason)
	}

	if rebuilt == nil {
		return body
	}
	rebuilt = append(rebuilt, ']')
	updated, err := sjson.SetRawBytes(body, "input", rebuilt)
	if err != nil {
		helps.LogWithRequestID(ctx).Debugf("%s: failed to rebuild input array while sanitizing reasoning encrypted_content: %v", provider, err)
		return body
	}
	return updated
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
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			remainingItems = append(remainingItems, item.Raw)
			continue
		}
		if !item.Get("encrypted_content").Exists() {
			remainingItems = append(remainingItems, item.Raw)
			continue
		}

		dropped = true
		itemID := strings.TrimSpace(item.Get("id").String())
		if itemID == "" {
			itemID = fmt.Sprintf("input[%d]", index)
		}
		helps.LogWithRequestID(ctx).Debugf("%s: dropped reasoning item at input[%d] item_id=%q reason=%s", provider, index, itemID, reason)
	}
	if !dropped {
		return body, false
	}

	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(remainingItems, ",")+"]"))
	if err != nil {
		helps.LogWithRequestID(ctx).Debugf("%s: failed to replace input after dropping reasoning items: %v", provider, err)
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
