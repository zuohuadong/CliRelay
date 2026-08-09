package handlers

import (
	"strings"

	"github.com/gin-gonic/gin"
	internalrouting "github.com/router-for-me/CLIProxyAPI/v7/internal/routing"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// FilterModelsByAccess filters available models by the API key's direct model allow-list,
// allowed channels, and allowed channel groups. If no restriction is set, it returns all models.
func (h *BaseAPIHandler) FilterModelsByAccess(c *gin.Context, allModels []map[string]any) []map[string]any {
	if c == nil {
		return allModels
	}
	raw, exists := c.Get("accessMetadata")
	if !exists {
		return allModels
	}
	metadata, ok := raw.(map[string]string)
	if !ok {
		return allModels
	}
	allowedModelsStr := metadata["allowed-models"]
	allowedParts := strings.Split(allowedModelsStr, ",")
	allowedModels := make([]string, 0, len(allowedParts))
	for _, part := range allowedParts {
		part = strings.TrimSpace(part)
		if part != "" {
			allowedModels = append(allowedModels, strings.ToLower(part))
		}
	}
	allowedChannels := normalizedAccessSet(metadata["allowed-channels"], func(value string) string {
		return strings.ToLower(strings.TrimSpace(value))
	})
	allowedGroups := normalizedAccessSet(metadata["allowed-channel-groups"], internalrouting.NormalizeGroupName)
	restrictByModels := len(allowedModels) > 0
	restrictByScopes := len(allowedChannels) > 0 || len(allowedGroups) > 0
	if restrictByScopes && h.AuthManager == nil {
		return []map[string]any{}
	}
	if !restrictByModels && !restrictByScopes {
		return allModels
	}

	filtered := make([]map[string]any, 0, len(allModels))
	for _, model := range allModels {
		if model == nil {
			continue
		}
		modelIDs := modelAccessIDs(model)
		if len(modelIDs) == 0 {
			continue
		}

		if restrictByModels && !modelMatchesAccessList(modelIDs, allowedModels) {
			continue
		}
		if restrictByScopes && !modelAvailableInAccessScopes(h.AuthManager, modelIDs, allowedChannels, allowedGroups) {
			continue
		}
		filtered = append(filtered, model)
	}
	return filtered
}

func normalizedAccessSet(raw string, normalizer func(string) string) map[string]struct{} {
	parts := strings.Split(raw, ",")
	values := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if normalizer != nil {
			value = normalizer(value)
		}
		if value != "" {
			values[value] = struct{}{}
		}
	}
	return values
}

func modelMatchesAccessList(modelIDs, allowedModels []string) bool {
	for _, modelID := range modelIDs {
		for _, allowed := range allowedModels {
			if allowed == modelID {
				return true
			}
			if strings.HasSuffix(allowed, "*") {
				prefix := strings.TrimSuffix(allowed, "*")
				if strings.HasPrefix(modelID, prefix) {
					return true
				}
			}
		}
	}
	return false
}

func modelAvailableInAccessScopes(manager *coreauth.Manager, modelIDs []string, allowedChannels, allowedGroups map[string]struct{}) bool {
	if manager == nil {
		return false
	}
	for _, modelID := range modelIDs {
		if manager.CanServeModelWithScopes(modelID, allowedChannels, allowedGroups, "") {
			return true
		}
	}
	return false
}

func modelAccessIDs(model map[string]any) []string {
	values := make([]string, 0, 3)
	for _, key := range []string{"id", "name"} {
		value, _ := model[key].(string)
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		values = append(values, value)
		if trimmed := strings.TrimPrefix(value, "models/"); trimmed != value {
			values = append(values, trimmed)
		}
	}
	return values
}
