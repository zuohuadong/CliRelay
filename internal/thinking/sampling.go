package thinking

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/sjson"
)

// NormalizeSamplingForReasoning strips sampling parameters that reasoning models
// reject. OpenAI GPT-5 family (and other reasoning models identified by a
// non-nil Thinking capability) only accept temperature=1 and reject top_p/top_k.
// Keeping a client-supplied temperature (e.g. 0 or 0.7) causes upstream errors:
//
//	invalid temperature: only 1 is allowed for this model
//
// This helper is intentionally provider-agnostic and only removes fields; it does
// not inject defaults, so providers that genuinely accept temperature pass through
// untouched for non-reasoning models.
func NormalizeSamplingForReasoning(body []byte, model string, providerKey string) []byte {
	suffixResult := ParseSuffix(model)
	baseModel := suffixResult.ModelName
	modelInfo := registry.LookupModelInfo(baseModel, providerKey)
	if modelInfo == nil || IsUserDefinedModel(modelInfo) || modelInfo.Thinking == nil {
		return body
	}

	body, _ = sjson.DeleteBytes(body, "temperature")
	body, _ = sjson.DeleteBytes(body, "top_p")
	body, _ = sjson.DeleteBytes(body, "top_k")
	return body
}
