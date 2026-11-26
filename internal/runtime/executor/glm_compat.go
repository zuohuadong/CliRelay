package executor

import (
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
