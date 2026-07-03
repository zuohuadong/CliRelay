package executor

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// parseBedrockClaudeUsage extracts an Anthropic-style usage object into usage.Detail.
// Bedrock responses carry usage under the top-level "usage" key.
func parseBedrockClaudeUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data).Get("usage")
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	return bedrockDetailFromUsageNode(usageNode)
}

// parseBedrockClaudeStreamUsage parses usage from a streaming "data:" line.
func parseBedrockClaudeStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayloadFromDataLine(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	return bedrockDetailFromUsageNode(usageNode), true
}

func bedrockDetailFromUsageNode(usageNode gjson.Result) usage.Detail {
	detail := usage.Detail{
		InputTokens:  usageNode.Get("input_tokens").Int(),
		OutputTokens: usageNode.Get("output_tokens").Int(),
	}
	if cacheRead := usageNode.Get("cache_read_input_tokens"); cacheRead.Exists() && cacheRead.Int() > 0 {
		detail.CacheReadTokens = cacheRead.Int()
		detail.CachedTokens = detail.CacheReadTokens
	}
	if cacheCreate := usageNode.Get("cache_creation_input_tokens"); cacheCreate.Exists() && cacheCreate.Int() > 0 {
		detail.CacheCreationTokens = cacheCreate.Int()
		if detail.CachedTokens == 0 {
			detail.CachedTokens = detail.CacheCreationTokens
		}
	}
	detail.TotalTokens = detail.InputTokens + detail.OutputTokens
	return detail
}
