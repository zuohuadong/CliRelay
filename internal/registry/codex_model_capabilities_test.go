package registry

import "testing"

func TestWithCodexBuiltinsIncludesGPT56Capabilities(t *testing.T) {
	models := WithCodexBuiltins(nil)
	want := map[string]string{
		"gpt-5.6":       "gpt-5.6-sol",
		"gpt-5.6-sol":   "gpt-5.6-sol",
		"gpt-5.6-terra": "gpt-5.6-terra",
		"gpt-5.6-luna":  "gpt-5.6-luna",
		"gpt-5.6-ultra": "gpt-5.6-sol",
	}
	for id, version := range want {
		model := modelByID(models, id)
		if model == nil {
			t.Fatalf("WithCodexBuiltins() missing %q", id)
		}
		if model.Version != version {
			t.Fatalf("%s version = %q, want %q", id, model.Version, version)
		}
		if model.ContextLength != 1_050_000 || model.MaxCompletionTokens != 128_000 {
			t.Fatalf("%s limits = %d/%d, want 1050000/128000", id, model.ContextLength, model.MaxCompletionTokens)
		}
		if model.Thinking == nil || !hasThinkingLevel(model.Thinking.Levels, "max") {
			t.Fatalf("%s thinking = %#v, want max support", id, model.Thinking)
		}
		if !hasThinkingLevel(model.Thinking.Levels, "ultra") {
			t.Fatalf("%s thinking = %#v, want ultra support", id, model.Thinking.Levels)
		}
	}
}

func modelByID(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

func hasThinkingLevel(levels []string, want string) bool {
	for _, level := range levels {
		if level == want {
			return true
		}
	}
	return false
}
