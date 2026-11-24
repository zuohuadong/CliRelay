package executor

import "github.com/router-for-me/CLIProxyAPI/v8/internal/config"

const miniMaxImageEndpoint = "/image_generation"

// MiniMaxExecutor keeps MiniMax's native image route while reusing the
// OpenAI-compatible chat and response implementation.
type MiniMaxExecutor struct {
	*OpenAICompatExecutor
}

func NewMiniMaxExecutor(cfg *config.Config) *MiniMaxExecutor {
	return &MiniMaxExecutor{
		OpenAICompatExecutor: newOpenAICompatExecutorWithImageEndpoint("minimax", cfg, miniMaxImageEndpoint),
	}
}

// OllamaCloudExecutor is a named compatibility executor so provider-specific
// routing remains observable without duplicating the OpenAI-compatible stack.
type OllamaCloudExecutor struct {
	*OpenAICompatExecutor
}

func NewOllamaCloudExecutor(cfg *config.Config) *OllamaCloudExecutor {
	return &OllamaCloudExecutor{
		OpenAICompatExecutor: NewOpenAICompatExecutor("ollama-cloud", cfg),
	}
}
