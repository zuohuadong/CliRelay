package registry

const (
	gpt56ContextWindow       = 1_050_000
	gpt56MaxCompletionTokens = 128_000
)

var gpt56RuntimeReasoningLevels = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

func codexBuiltinGPT56ModelInfos() []*ModelInfo {
	return []*ModelInfo{
		newGPT56ModelInfo("gpt-5.6", "gpt-5.6-sol", "GPT-5.6", "GPT-5.6 family alias routed to GPT-5.6 Sol."),
		newGPT56ModelInfo("gpt-5.6-sol", "gpt-5.6-sol", "GPT-5.6 Sol", "Frontier GPT-5.6 model for complex professional work."),
		newGPT56ModelInfo("gpt-5.6-terra", "gpt-5.6-terra", "GPT-5.6 Terra", "Balanced GPT-5.6 model for everyday professional work."),
		newGPT56ModelInfo("gpt-5.6-luna", "gpt-5.6-luna", "GPT-5.6 Luna", "Fast GPT-5.6 model for high-volume work."),
		newGPT56ModelInfo("gpt-5.6-ultra", "gpt-5.6-sol", "GPT-5.6 Sol (Ultra compatibility alias)", "Compatibility alias for GPT-5.6 Sol."),
	}
}

func newGPT56ModelInfo(id, target, displayName, description string) *ModelInfo {
	return &ModelInfo{
		ID:                  id,
		Object:              "model",
		Created:             1767225600, // 2026-01-01
		OwnedBy:             "openai",
		Type:                "openai",
		DisplayName:         displayName,
		Name:                id,
		Version:             target,
		Description:         description,
		ContextLength:       gpt56ContextWindow,
		InputTokenLimit:     gpt56ContextWindow,
		MaxCompletionTokens: gpt56MaxCompletionTokens,
		OutputTokenLimit:    gpt56MaxCompletionTokens,
		SupportedParameters: []string{"tools"},
		Thinking: &ThinkingSupport{
			Levels: append([]string(nil), gpt56RuntimeReasoningLevels...),
		},
	}
}
