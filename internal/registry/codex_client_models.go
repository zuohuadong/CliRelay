package registry

import (
	"encoding/json"
	"strings"

	_ "embed"
)

//go:embed models/codex_client_models.json
var codexClientModelsJSON []byte

// GetCodexClientModelsJSON returns the embedded Codex client model catalog.
func GetCodexClientModelsJSON() []byte {
	return append([]byte(nil), codexClientModelsJSON...)
}

type codexClientModelCatalog struct {
	Models []codexClientModelTemplate `json:"models"`
}

type codexClientModelTemplate struct {
	Slug                     string   `json:"slug"`
	DisplayName              string   `json:"display_name"`
	Description              string   `json:"description"`
	Visibility               string   `json:"visibility"`
	ContextWindow            int      `json:"context_window"`
	MaxContextWindow         int      `json:"max_context_window"`
	InputModalities          []string `json:"input_modalities"`
	OutputModalities         []string `json:"output_modalities"`
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

func getCodexClientListedModelInfos() []*ModelInfo {
	var catalog codexClientModelCatalog
	if err := json.Unmarshal(codexClientModelsJSON, &catalog); err != nil {
		return nil
	}

	out := make([]*ModelInfo, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		if strings.EqualFold(strings.TrimSpace(model.Visibility), "hide") {
			continue
		}
		slug := strings.TrimSpace(model.Slug)
		if slug == "" {
			continue
		}
		displayName := strings.TrimSpace(model.DisplayName)
		if displayName == "" {
			displayName = slug
		}
		contextWindow := model.ContextWindow
		if model.MaxContextWindow > contextWindow {
			contextWindow = model.MaxContextWindow
		}
		out = append(out, &ModelInfo{
			ID:                        slug,
			Object:                    "model",
			Created:                   1704067200,
			OwnedBy:                   "openai",
			Type:                      "openai",
			DisplayName:               displayName,
			Version:                   slug,
			Description:               strings.TrimSpace(model.Description),
			ContextLength:             contextWindow,
			SupportedInputModalities:  cloneStrings(model.InputModalities),
			SupportedOutputModalities: cloneStrings(model.OutputModalities),
			Thinking:                  codexClientThinkingSupport(model),
		})
	}
	return out
}

func codexClientThinkingSupport(model codexClientModelTemplate) *ThinkingSupport {
	if len(model.SupportedReasoningLevels) == 0 {
		return nil
	}
	levels := make([]string, 0, len(model.SupportedReasoningLevels))
	seen := make(map[string]struct{}, len(model.SupportedReasoningLevels))
	for _, level := range model.SupportedReasoningLevels {
		effort := strings.TrimSpace(level.Effort)
		if effort == "" {
			continue
		}
		key := strings.ToLower(effort)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		levels = append(levels, effort)
	}
	if len(levels) == 0 {
		return nil
	}
	return &ThinkingSupport{Levels: levels}
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
