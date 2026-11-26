package executor

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// glm53ReasoningEfforts maps the effort levels used by clients to the three
// values GLM-5.3 and GLM-5.3-Flash accept. The upstream rejects everything
// else with `field ReasoningEffort invalid, should be one of: low, high, max`,
// and the Chinese BigModel endpoint reports the same rejection as
// "API 调用参数有误，请检查文档。".
//
// Codex sends `xhigh` for its extra-high reasoning level, and Claude Code's
// thinking budgets translate to `medium`/`xhigh`, so a GLM-5.3 request that
// looks fine on other models fails before the model ever runs.
var glm53ReasoningEfforts = map[string]string{
	"minimal": "low",
	"low":     "low",
	"medium":  "high",
	"high":    "high",
	"xhigh":   "max",
	"max":     "max",
}

// normalizeGLMReasoningEffort rewrites reasoning_effort for models that only
// accept a subset of the OpenAI effort vocabulary. It is a no-op for every
// other model, including GLM-5.2 and GLM-5.4, which accept all levels.
func normalizeGLMReasoningEffort(payload []byte, model string) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) || !isGLM53Model(model) {
		return payload
	}
	effort := gjson.GetBytes(payload, "reasoning_effort")
	if !effort.Exists() {
		return payload
	}
	value := strings.ToLower(strings.TrimSpace(effort.String()))
	switch value {
	case "", "none":
		return payload
	case "auto":
		// "auto" is an internal marker, never a wire value.
		updated, err := sjson.DeleteBytes(payload, "reasoning_effort")
		if err != nil {
			return payload
		}
		return updated
	}
	mapped, ok := glm53ReasoningEfforts[value]
	if !ok {
		updated, err := sjson.DeleteBytes(payload, "reasoning_effort")
		if err != nil {
			return payload
		}
		return updated
	}
	if mapped == value {
		return payload
	}
	updated, err := sjson.SetBytes(payload, "reasoning_effort", mapped)
	if err != nil {
		return payload
	}
	return updated
}

// isGLM53Model reports whether the model is GLM-5.3 or a GLM-5.3 variant such
// as GLM-5.3-Flash. Provider prefixes ("zai-org/GLM-5.3") and thinking
// suffixes ("glm-5.3(xhigh)") are stripped first.
func isGLM53Model(model string) bool {
	base := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	return base == "glm-5.3" || strings.HasPrefix(base, "glm-5.3-")
}

// stripGLMCodingImageParts replaces image content parts with a text note before
// a GLM coding-plan endpoint sees them. Those endpoints serve text-only
// GLM-5.x models and answer any image with code 1210
// "API 调用参数有误，请检查文档。", which reads like an invalid parameter even
// though the request is otherwise well formed.
//
// Codex's view_image tool output is the usual source: it returns an
// input_image part whose data URL rides along in the conversation history long
// after the turn that captured it.
func stripGLMCodingImageParts(payload []byte, baseURL, model string) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) || !isGLMCodingTextOnly(baseURL, model) {
		return payload
	}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}
	note := `{"type":"text","text":"[image omitted: this GLM coding model does not accept image input]"}`
	updated := payload
	for i, message := range messages.Array() {
		parts := message.Get("content")
		if !parts.IsArray() {
			continue
		}
		for j, part := range parts.Array() {
			switch strings.ToLower(strings.TrimSpace(part.Get("type").String())) {
			case "image", "image_url", "input_image":
				path := fmt.Sprintf("messages.%d.content.%d", i, j)
				result, err := sjson.SetRawBytes(updated, path, []byte(note))
				if err != nil {
					return payload
				}
				updated = result
			}
		}
	}
	return updated
}

// isGLMCodingTextOnly reports whether the upstream is a GLM coding-plan
// endpoint serving text-only GLM-5.x models. Standard BigModel and Z.AI API
// endpoints are left alone because they can serve vision models.
func isGLMCodingTextOnly(baseURL, model string) bool {
	url := strings.ToLower(strings.TrimSpace(baseURL))
	if !strings.Contains(url, "/api/coding/") {
		return false
	}
	if !strings.Contains(url, "bigmodel.cn") && !strings.Contains(url, "z.ai") {
		return false
	}
	base := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	return strings.HasPrefix(base, "glm-5")
}
