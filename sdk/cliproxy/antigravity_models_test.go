package cliproxy

import "testing"

func TestParseAntigravityFetchedModelsIncludesHighModel(t *testing.T) {
	body := []byte(`{
		"webSearchModelIds": ["gemini-3.1-pro-high"],
		"models": {
			"gemini-3.1-pro-high": {
				"displayName": "Gemini 3.1 Pro (High)",
				"maxTokens": 1048576,
				"maxOutputTokens": 65535
			},
			"tab_internal": {
				"displayName": "Internal",
				"maxTokens": 10
			}
		}
	}`)

	result := parseAntigravityFetchedModels(body)
	if len(result.Models) != 1 {
		t.Fatalf("expected 1 public model, got %d", len(result.Models))
	}
	model := result.Models[0]
	if model.ID != "gemini-3.1-pro-high" {
		t.Fatalf("model ID = %q, want gemini-3.1-pro-high", model.ID)
	}
	if model.DisplayName != "Gemini 3.1 Pro (High)" {
		t.Fatalf("display name = %q, want Gemini 3.1 Pro (High)", model.DisplayName)
	}
	if model.ContextLength != 1048576 {
		t.Fatalf("context length = %d, want 1048576", model.ContextLength)
	}
	if model.MaxCompletionTokens != 65535 {
		t.Fatalf("max completion tokens = %d, want 65535", model.MaxCompletionTokens)
	}
	if !model.SupportsWebSearch {
		t.Fatal("expected web search capability from fetched hints")
	}
	if model.Thinking == nil {
		t.Fatal("expected thinking config for high model")
	}
}
