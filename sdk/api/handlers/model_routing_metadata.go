package handlers

import (
	"strings"

	executorhelps "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func setEstimatedInputTokensMetadata(meta map[string]any, modelName string, rawJSON []byte) {
	if meta == nil || len(rawJSON) == 0 {
		return
	}
	if _, exists := meta[coreexecutor.EstimatedInputTokensMetadataKey]; exists {
		return
	}
	count := estimateInputTokensForRouting(modelName, rawJSON)
	if count > 0 {
		meta[coreexecutor.EstimatedInputTokensMetadataKey] = count
	}
}

func estimateInputTokensForRouting(modelName string, rawJSON []byte) int64 {
	if len(rawJSON) == 0 {
		return 0
	}
	enc, err := executorhelps.TokenizerForModel(modelName)
	if err == nil && enc != nil {
		if count, errCount := executorhelps.CountOpenAIChatTokens(enc, rawJSON); errCount == nil && count > 0 {
			return count
		}
		if count, errCount := enc.Count(strings.TrimSpace(string(rawJSON))); errCount == nil && count > 0 {
			return int64(count)
		}
	}
	return int64((len(rawJSON) + 3) / 4)
}
