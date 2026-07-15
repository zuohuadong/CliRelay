package handlers

import (
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
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
			{"type":"image_generation"}
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

func TestEnrichRequestExecutionMetadataTreatsWebSearchPreviewAsXunfeiSupported(t *testing.T) {
	meta := make(map[string]any)

	enrichRequestExecutionMetadata(meta, []byte(`{
		"model":"gpt-5.3-codex",
		"input":[{"role":"user","content":"search for docs"}],
		"tool_choice":"auto",
		"tools":[
			{"type":"function","name":"read_file","parameters":{"type":"object"}},
			{"type":"web_search_preview","search_context_size":"high"}
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

func TestFilterModelsByAccess(t *testing.T) {
	h := &BaseAPIHandler{}

	models := []map[string]any{
		{"id": "gpt-5.3-codex"},
		{"id": "glm-5.1"},
		{"id": "gemini-3-flash"},
		{"id": "gpt-5.4"},
		{"name": "models/gemini-3-pro"},
	}

	// 1. nil Context
	res1 := h.FilterModelsByAccess(nil, models)
	if len(res1) != len(models) {
		t.Fatalf("nil context: expected %d, got %d", len(models), len(res1))
	}

	// 2. No accessMetadata
	c2 := &gin.Context{}
	res2 := h.FilterModelsByAccess(c2, models)
	if len(res2) != len(models) {
		t.Fatalf("no metadata: expected %d, got %d", len(models), len(res2))
	}

	// 3. Metadata with no allowed-models
	c3 := &gin.Context{}
	c3.Set("accessMetadata", map[string]string{})
	res3 := h.FilterModelsByAccess(c3, models)
	if len(res3) != len(models) {
		t.Fatalf("no allowed-models: expected %d, got %d", len(models), len(res3))
	}

	// 4. Allowed-models exact match
	c4 := &gin.Context{}
	c4.Set("accessMetadata", map[string]string{"allowed-models": "gpt-5.3-codex,glm-5.1"})
	res4 := h.FilterModelsByAccess(c4, models)
	if len(res4) != 2 {
		t.Fatalf("exact match: expected 2, got %d", len(res4))
	}
	if res4[0]["id"] != "gpt-5.3-codex" || res4[1]["id"] != "glm-5.1" {
		t.Fatalf("exact match: unexpected elements: %v", res4)
	}

	// 5. Allowed-models wildcard match
	c5 := &gin.Context{}
	c5.Set("accessMetadata", map[string]string{"allowed-models": "gpt-*"})
	res5 := h.FilterModelsByAccess(c5, models)
	if len(res5) != 2 {
		t.Fatalf("wildcard match: expected 2, got %d", len(res5))
	}
	if res5[0]["id"] != "gpt-5.3-codex" || res5[1]["id"] != "gpt-5.4" {
		t.Fatalf("wildcard match: unexpected elements: %v", res5)
	}

	// 6. Gemini name fields may include a models/ prefix.
	c6 := &gin.Context{}
	c6.Set("accessMetadata", map[string]string{"allowed-models": "gemini-3-pro"})
	res6 := h.FilterModelsByAccess(c6, models)
	if len(res6) != 1 {
		t.Fatalf("gemini name match: expected 1, got %d", len(res6))
	}
	if res6[0]["name"] != "models/gemini-3-pro" {
		t.Fatalf("gemini name match: unexpected elements: %v", res6)
	}

	// 7. Scoped access without an auth manager must fail closed.
	c7 := &gin.Context{}
	c7.Set("accessMetadata", map[string]string{"allowed-channel-groups": "team"})
	res7 := h.FilterModelsByAccess(c7, models)
	if len(res7) != 0 {
		t.Fatalf("missing auth manager: expected 0, got %d", len(res7))
	}
}
