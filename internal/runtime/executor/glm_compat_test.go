package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeGLMReasoningEffort(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		in     string
		effort string
		exists bool
	}{
		{
			name:   "glm-5.3 xhigh maps to max",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","reasoning_effort":"xhigh"}`,
			effort: "max",
			exists: true,
		},
		{
			name:   "glm-5.3 medium maps to high",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","reasoning_effort":"medium"}`,
			effort: "high",
			exists: true,
		},
		{
			name:   "glm-5.3 minimal maps to low",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","reasoning_effort":"minimal"}`,
			effort: "low",
			exists: true,
		},
		{
			name:   "glm-5.3 low is kept",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","reasoning_effort":"low"}`,
			effort: "low",
			exists: true,
		},
		{
			name:   "glm-5.3 high is kept",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","reasoning_effort":"high"}`,
			effort: "high",
			exists: true,
		},
		{
			name:   "glm-5.3 max is kept",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","reasoning_effort":"max"}`,
			effort: "max",
			exists: true,
		},
		{
			name:   "glm-5.3 none is kept",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","reasoning_effort":"none"}`,
			effort: "none",
			exists: true,
		},
		{
			name:   "glm-5.3 uppercase effort is normalized",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","reasoning_effort":"XHIGH"}`,
			effort: "max",
			exists: true,
		},
		{
			name:   "glm-5.3 flash is covered",
			model:  "glm-5.3-flash",
			in:     `{"model":"glm-5.3-flash","reasoning_effort":"xhigh"}`,
			effort: "max",
			exists: true,
		},
		{
			name:   "provider prefix is stripped",
			model:  "zai-org/GLM-5.3",
			in:     `{"model":"zai-org/GLM-5.3","reasoning_effort":"medium"}`,
			effort: "high",
			exists: true,
		},
		{
			name:   "thinking suffix is stripped",
			model:  "glm-5.3(xhigh)",
			in:     `{"model":"glm-5.3","reasoning_effort":"xhigh"}`,
			effort: "max",
			exists: true,
		},
		{
			name:   "unknown effort is dropped",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","reasoning_effort":"bogus"}`,
			exists: false,
		},
		{
			name:   "auto is dropped",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","reasoning_effort":"auto"}`,
			exists: false,
		},
		{
			name:   "glm-5.4 keeps every level",
			model:  "glm-5.4",
			in:     `{"model":"glm-5.4","reasoning_effort":"xhigh"}`,
			effort: "xhigh",
			exists: true,
		},
		{
			name:   "glm-5.2 keeps every level",
			model:  "glm-5.2",
			in:     `{"model":"glm-5.2","reasoning_effort":"medium"}`,
			effort: "medium",
			exists: true,
		},
		{
			name:   "other models are untouched",
			model:  "deepseek-v4.1-flash",
			in:     `{"model":"deepseek-v4.1-flash","reasoning_effort":"xhigh"}`,
			effort: "xhigh",
			exists: true,
		},
		{
			name:   "missing effort stays missing",
			model:  "glm-5.3",
			in:     `{"model":"glm-5.3","messages":[]}`,
			exists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeGLMReasoningEffort([]byte(tt.in), tt.model)
			effort := gjson.GetBytes(got, "reasoning_effort")
			if effort.Exists() != tt.exists {
				t.Fatalf("reasoning_effort exists = %v, want %v (payload %s)", effort.Exists(), tt.exists, got)
			}
			if tt.exists && effort.String() != tt.effort {
				t.Fatalf("reasoning_effort = %q, want %q", effort.String(), tt.effort)
			}
		})
	}
}

func TestNormalizeGLMReasoningEffortKeepsInvalidPayload(t *testing.T) {
	in := []byte(`{"model":"glm-5.3",`)
	if got := normalizeGLMReasoningEffort(in, "glm-5.3"); string(got) != string(in) {
		t.Fatalf("invalid payload changed: %s", got)
	}
	if got := normalizeGLMReasoningEffort(nil, "glm-5.3"); len(got) != 0 {
		t.Fatalf("nil payload changed: %s", got)
	}
}

func TestStripGLMCodingImagePartsReplacesImages(t *testing.T) {
	in := []byte(`{
		"model":"glm-5.4",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},
			{"role":"tool","tool_call_id":"call_1","content":[{"type":"input_image","image_url":"data:image/png;base64,BBBB"}]}
		]
	}`)
	got := stripGLMCodingImageParts(in, "https://open.bigmodel.cn/api/coding/paas/v4", "glm-5.4")
	if gjson.GetBytes(got, "messages.0.content.1.type").String() != "text" {
		t.Fatalf("user image was not replaced: %s", got)
	}
	if gjson.GetBytes(got, "messages.1.content.0.type").String() != "text" {
		t.Fatalf("tool image was not replaced: %s", got)
	}
	if gjson.GetBytes(got, "messages.0.content.0.text").String() != "look" {
		t.Fatalf("text part changed: %s", got)
	}
}

func TestStripGLMCodingImagePartsLeavesOtherEndpointsAlone(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	for _, baseURL := range []string{
		"https://open.bigmodel.cn/api/paas/v4",
		"https://api.z.ai/api/paas/v4",
		"https://example.com/api/coding/paas/v4",
	} {
		if got := stripGLMCodingImageParts(in, baseURL, "glm-5.4"); string(got) != string(in) {
			t.Fatalf("payload changed for %s: %s", baseURL, got)
		}
	}
	if got := stripGLMCodingImageParts(in, "https://open.bigmodel.cn/api/coding/paas/v4", "glm-4.6v"); string(got) != string(in) {
		t.Fatalf("vision model payload changed: %s", got)
	}
}
