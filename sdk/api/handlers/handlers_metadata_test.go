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

func TestEnrichRequestExecutionMetadataMarksRequiredAndXunfeiUnsupportedTools(t *testing.T) {
	meta := make(map[string]any)

	enrichRequestExecutionMetadata(meta, []byte(`{
		"model":"gpt-5.3-codex",
		"input":[{"role":"user","content":"use tools"}],
		"tool_choice":"required",
		"tools":[
			{"type":"function","name":"read_file","parameters":{"type":"object"}},
			{"type":"web_search_preview"}
		]
	}`))

	features, ok := meta[coreexecutor.RequestFeaturesMetadataKey].([]string)
	if !ok {
		t.Fatalf("RequestFeaturesMetadataKey = %#v, want []string", meta[coreexecutor.RequestFeaturesMetadataKey])
	}
	for _, want := range []string{"tools", "required-tools", "xunfei-unsupported-tools"} {
		if !slices.Contains(features, want) {
			t.Fatalf("features = %v, want %q", features, want)
		}
	}
}

func TestEnrichRequestExecutionMetadataAllowsForcedFunctionToolChoiceForAstron(t *testing.T) {
	meta := make(map[string]any)

	enrichRequestExecutionMetadata(meta, []byte(`{
		"model":"gpt-5.3-codex",
		"input":[{"role":"user","content":"use the read_file tool"}],
		"tool_choice":{"type":"function","function":{"name":"read_file"}},
		"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]
	}`))

	features, ok := meta[coreexecutor.RequestFeaturesMetadataKey].([]string)
	if !ok {
		t.Fatalf("RequestFeaturesMetadataKey = %#v, want []string", meta[coreexecutor.RequestFeaturesMetadataKey])
	}
	if slices.Contains(features, "required-tools") {
		t.Fatalf("features = %v, did not want required-tools", features)
	}
}

func TestEnrichRequestExecutionMetadataKeepsXunfeiSupportedAutoTools(t *testing.T) {
	meta := make(map[string]any)

	enrichRequestExecutionMetadata(meta, []byte(`{
		"model":"gpt-5.3-codex",
		"input":[{"role":"user","content":"use tools"}],
		"tool_choice":"auto",
		"tools":[
			{"type":"function","name":"read_file","parameters":{"type":"object"}},
			{"type":"web_search"}
		]
	}`))

	features, ok := meta[coreexecutor.RequestFeaturesMetadataKey].([]string)
	if !ok {
		t.Fatalf("RequestFeaturesMetadataKey = %#v, want []string", meta[coreexecutor.RequestFeaturesMetadataKey])
	}
	if !slices.Contains(features, "tools") {
		t.Fatalf("features = %v, want tools", features)
	}
	for _, unexpected := range []string{"required-tools", "xunfei-unsupported-tools", "mcp"} {
		if slices.Contains(features, unexpected) {
			t.Fatalf("features = %v, did not want %q", features, unexpected)
		}
	}
}

func TestEnrichRequestExecutionMetadataCompactIgnoresHistoricalToolFeatures(t *testing.T) {
	meta := make(map[string]any)

	enrichRequestExecutionMetadataForAlt(meta, []byte(`{
		"model":"gpt-5.3-codex",
		"input":[{"role":"user","content":"hi"}],
		"tool_choice":"required",
		"tools":[
			{"type":"mcp","server_label":"web-reader"},
			{"type":"web_search_preview"}
		]
	}`), "responses/compact")

	if got := meta[coreexecutor.InputItemsMetadataKey]; got != 1 {
		t.Fatalf("InputItemsMetadataKey = %v, want 1", got)
	}
	if got := meta[coreexecutor.ToolDefinitionsMetadataKey]; got != 2 {
		t.Fatalf("ToolDefinitionsMetadataKey = %v, want 2", got)
	}
	if features, ok := meta[coreexecutor.RequestFeaturesMetadataKey].([]string); ok && len(features) > 0 {
		t.Fatalf("compact features = %v, want none", features)
	}
}

func TestSetServiceTierMetadataExtractsValue(t *testing.T) {
	meta := make(map[string]any)

	setServiceTierMetadata(meta, []byte(`{"service_tier":"priority"}`))

	gotServiceTier := meta[coreexecutor.ServiceTierMetadataKey]
	if gotServiceTier != "priority" {
		t.Fatalf("ServiceTierMetadataKey = %v, want %q", gotServiceTier, "priority")
	}
}

func TestSetServiceTierMetadataDefaultsWhenMissing(t *testing.T) {
	meta := make(map[string]any)

	setServiceTierMetadata(meta, []byte(`{"model":"gpt-5.4"}`))

	gotServiceTier := meta[coreexecutor.ServiceTierMetadataKey]
	if gotServiceTier != "default" {
		t.Fatalf("ServiceTierMetadataKey = %v, want %q", gotServiceTier, "default")
	}
}
