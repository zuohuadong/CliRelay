package modelconfig

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// ResolveModelInfo returns a private capability snapshot for a configured model.
// Static capabilities come from the suffix-free upstream name, while explicit
// configuration takes precedence.
func ResolveModelInfo(name, modelType string, support *registry.ThinkingSupport) *registry.ModelInfo {
	trimmedName := strings.TrimSpace(name)
	baseName := strings.TrimSpace(thinking.ParseSuffix(trimmedName).ModelName)
	staticInfo := registry.LookupStaticModelInfo(baseName)
	info := cloneModelInfo(staticInfo)
	if info == nil {
		info = &registry.ModelInfo{}
	}
	info.ID = trimmedName
	info.Type = strings.TrimSpace(modelType)
	if support != nil {
		normalized := NormalizeThinkingSupport(support)
		// Configured levels replace the catalog entry, but the way a known model
		// turns thinking off is an upstream API fact, not a level choice.
		if normalized.DisableType == "" && info.Thinking != nil {
			normalized.DisableType = info.Thinking.DisableType
			if normalized.DisableType == "" && info.Thinking.Adaptive != nil &&
				info.Thinking.Adaptive.Off == registry.ClaudeThinkingOffBetweenTools {
				normalized.DisableType = info.Thinking.Adaptive.Off
			}
		}
		info.Thinking = normalized
	}
	info.UserDefined = false
	return info
}

func cloneModelInfo(info *registry.ModelInfo) *registry.ModelInfo {
	if info == nil {
		return nil
	}
	copyInfo := *info
	copyInfo.SupportedGenerationMethods = append([]string(nil), info.SupportedGenerationMethods...)
	copyInfo.SupportedParameters = append([]string(nil), info.SupportedParameters...)
	copyInfo.SupportedInputModalities = append([]string(nil), info.SupportedInputModalities...)
	copyInfo.SupportedOutputModalities = append([]string(nil), info.SupportedOutputModalities...)
	if info.Thinking != nil {
		thinking := *info.Thinking
		thinking.Levels = append([]string(nil), info.Thinking.Levels...)
		if info.Thinking.Adaptive != nil {
			adaptive := *info.Thinking.Adaptive
			thinking.Adaptive = &adaptive
		}
		copyInfo.Thinking = &thinking
	}
	if info.NativeCapabilities != nil {
		capabilities := *info.NativeCapabilities
		copyInfo.NativeCapabilities = &capabilities
	}
	if info.Config != nil {
		config := *info.Config
		config.OverrideHeader = make(map[string]string, len(info.Config.OverrideHeader))
		for key, value := range info.Config.OverrideHeader {
			config.OverrideHeader[key] = value
		}
		copyInfo.Config = &config
	}
	return &copyInfo
}

// NormalizeThinkingSupport clones and normalizes configured reasoning levels.
func NormalizeThinkingSupport(raw *registry.ThinkingSupport) *registry.ThinkingSupport {
	if raw == nil {
		return nil
	}
	normalized := *raw
	normalized.Levels = nil
	seen := make(map[string]struct{}, len(raw.Levels))
	for _, value := range raw.Levels {
		level := strings.ToLower(strings.TrimSpace(value))
		if level == "" {
			continue
		}
		switch level {
		case "none":
			normalized.ZeroAllowed = true
		case "auto":
			normalized.DynamicAllowed = true
		}
		if _, exists := seen[level]; exists {
			continue
		}
		seen[level] = struct{}{}
		normalized.Levels = append(normalized.Levels, level)
	}
	return &normalized
}
