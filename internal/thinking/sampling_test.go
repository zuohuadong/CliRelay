package thinking

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeSamplingForReasoning_StripsTemperatureForGPT5(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[]}`)
	out := NormalizeSamplingForReasoning(body, "gpt-5.4", "openai")
	if gjson.GetBytes(out, "temperature").Exists() {
		t.Fatalf("temperature should be removed for reasoning model, got %s", out)
	}
	if gjson.GetBytes(out, "top_p").Exists() {
		t.Fatalf("top_p should be removed for reasoning model, got %s", out)
	}
	if gjson.GetBytes(out, "top_k").Exists() {
		t.Fatalf("top_k should be removed for reasoning model, got %s", out)
	}
}

func TestNormalizeSamplingForReasoning_KeepsTemperatureForNonReasoning(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","temperature":0.7,"top_p":0.9,"messages":[]}`)
	out := NormalizeSamplingForReasoning(body, "gpt-4o", "openai")
	if got := gjson.GetBytes(out, "temperature").Float(); got != 0.7 {
		t.Fatalf("temperature should be preserved for non-reasoning model, got %v", got)
	}
}

func TestNormalizeSamplingForReasoning_StripsForSuffixModel(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","temperature":0,"top_p":0.9,"messages":[]}`)
	out := NormalizeSamplingForReasoning(body, "gpt-5.4(high)", "openai")
	if gjson.GetBytes(out, "temperature").Exists() {
		t.Fatalf("temperature should be removed for suffix reasoning model, got %s", out)
	}
}
