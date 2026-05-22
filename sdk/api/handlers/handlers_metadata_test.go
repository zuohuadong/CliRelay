package handlers

import (
	"slices"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadataIncludesExecutionSessionWithoutIdempotencyKey(t *testing.T) {
	ctx := WithExecutionSessionID(context.Background(), "session-1")

	meta := requestExecutionMetadata(ctx)
	if got := meta[coreexecutor.ExecutionSessionMetadataKey]; got != "session-1" {
		t.Fatalf("ExecutionSessionMetadataKey = %v, want %q", got, "session-1")
	}
	if _, ok := meta[idempotencyKeyMetadataKey]; ok {
		t.Fatalf("unexpected idempotency key in metadata: %v", meta[idempotencyKeyMetadataKey])
	}
}

func TestSetReasoningEffortMetadataUsesSuffixOverBody(t *testing.T) {
	meta := make(map[string]any)

	setReasoningEffortMetadata(meta, "openai", "gpt-5.4(high)", []byte(`{"reasoning_effort":"low"}`))

	if got := meta[coreexecutor.ReasoningEffortMetadataKey]; got != "high" {
		t.Fatalf("ReasoningEffortMetadataKey = %v, want %q", got, "high")
	}
}

func TestSetReasoningEffortMetadataSupportsOpenAIResponses(t *testing.T) {
	meta := make(map[string]any)

	setReasoningEffortMetadata(meta, "openai-response", "gpt-5.4", []byte(`{"reasoning":{"effort":"medium"}}`))

	if got := meta[coreexecutor.ReasoningEffortMetadataKey]; got != "medium" {
		t.Fatalf("ReasoningEffortMetadataKey = %v, want %q", got, "medium")
	}
}
<<<<<<< HEAD

func TestEnrichRequestExecutionMetadataMarksMCPTools(t *testing.T) {
	meta := make(map[string]any)

	enrichRequestExecutionMetadata(meta, []byte(`{
		"model":"gpt-5.3-codex",
		"input":[{"role":"user","content":"use mcp"}],
		"tools":[{"type":"mcp","server_label":"web-reader"}]
	}`))

	features, ok := meta[coreexecutor.RequestFeaturesMetadataKey].([]string)
	if !ok {
		t.Fatalf("RequestFeaturesMetadataKey = %#v, want []string", meta[coreexecutor.RequestFeaturesMetadataKey])
	}
	for _, want := range []string{"tools", "mcp"} {
		if !slices.Contains(features, want) {
			t.Fatalf("features = %v, want %q", features, want)
		}
	}
}
=======
>>>>>>> upstream/main
