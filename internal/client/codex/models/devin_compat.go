package models

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func applyCodexClientDevinDisplayName(entry map[string]any, id string, model map[string]any, providersForModel ProvidersForModelFunc) {
	if !isCodexClientDevinModel(id, model, entry, providersForModel) {
		return
	}
	displayName := stringModelValue(entry, "display_name")
	if displayName == "" {
		displayName = id
	}
	trimmed := strings.TrimSpace(displayName)
	if strings.HasSuffix(trimmed, " (Devin)") {
		return
	}
	if strings.HasSuffix(strings.ToLower(trimmed), " (devin)") {
		entry["display_name"] = trimmed[:len(trimmed)-len(" (devin)")] + " (Devin)"
		return
	}
	if strings.HasSuffix(strings.ToLower(trimmed), "(devin)") {
		entry["display_name"] = strings.TrimSpace(trimmed[:len(trimmed)-len("(devin)")]) + " (Devin)"
		return
	}
	entry["display_name"] = trimmed + " (Devin)"
}

func isCodexClientDevinModel(id string, model map[string]any, entry map[string]any, providersForModel ProvidersForModelFunc) bool {
	idLower := strings.ToLower(strings.TrimSpace(id))
	if strings.HasPrefix(idLower, "devin/") {
		return true
	}
	if idx := strings.Index(idLower, "/"); idx != -1 {
		if strings.HasPrefix(idLower[idx+1:], "devin/") {
			return true
		}
	}
	if entry != nil {
		slugLower := strings.ToLower(strings.TrimSpace(stringModelValue(entry, "slug")))
		if strings.HasPrefix(slugLower, "devin/") {
			return true
		}
		if idx := strings.Index(slugLower, "/"); idx != -1 {
			if strings.HasPrefix(slugLower[idx+1:], "devin/") {
				return true
			}
		}
		if strings.EqualFold(strings.TrimSpace(stringModelValue(entry, "type")), "devin") ||
			strings.EqualFold(strings.TrimSpace(stringModelValue(entry, "owned_by")), "cognition") {
			return true
		}
	}
	if model != nil {
		if strings.EqualFold(strings.TrimSpace(stringModelValue(model, "type")), "devin") ||
			strings.EqualFold(strings.TrimSpace(stringModelValue(model, "owned_by")), "cognition") {
			return true
		}
	}
	if info := registry.LookupModelInfo(id); info != nil {
		if strings.EqualFold(info.Type, "devin") || strings.EqualFold(info.OwnedBy, "cognition") || strings.HasPrefix(strings.ToLower(info.ID), "devin/") {
			return true
		}
	} else if idx := strings.Index(id, "/"); idx != -1 {
		if info := registry.LookupModelInfo(strings.TrimSpace(id[idx+1:])); info != nil {
			if strings.EqualFold(info.Type, "devin") || strings.EqualFold(info.OwnedBy, "cognition") || strings.HasPrefix(strings.ToLower(info.ID), "devin/") {
				return true
			}
		}
	}
	if providersForModel != nil {
		providers := providersForModel(id)
		if len(providers) == 0 && strings.Contains(id, "/") {
			providers = providersForModel(strings.TrimSpace(id[strings.Index(id, "/")+1:]))
		}
		for _, p := range providers {
			if strings.EqualFold(strings.TrimSpace(p), "devin") {
				return true
			}
		}
	}
	return false
}

func nullCodexClientRequiredOptions(entry map[string]any) {
	entry["apply_patch_tool_type"] = nil
	entry["upgrade"] = nil
	entry["availability_nux"] = nil
}

const codexClientFallbackInstructions = "You are Codex, a coding agent. You and the user share one workspace."

func useCompactCodexClientInstructions(entry map[string]any) {
	entry["base_instructions"] = codexClientFallbackInstructions
	entry["model_messages"] = map[string]any{
		"instructions_template":  codexClientFallbackInstructions,
		"instructions_variables": nil,
		"approvals":              nil,
		"collaboration_modes":    nil,
		"auto_review":            nil,
		"permissions":            nil,
		"multi_agent":            nil,
	}
}
