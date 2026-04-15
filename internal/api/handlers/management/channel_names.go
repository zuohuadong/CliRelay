package management

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	internalrouting "github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func normalizeChannelName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func authChannelLabelFromMetadata(metadata map[string]any, provider string) string {
	if metadata != nil {
		if raw, ok := metadata["label"].(string); ok {
			if label := strings.TrimSpace(raw); label != "" {
				return label
			}
		}
		if raw, ok := metadata["email"].(string); ok {
			if email := strings.TrimSpace(raw); email != "" {
				return email
			}
		}
	}
	return strings.TrimSpace(provider)
}

func uniqueChannels(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		key := strings.ToLower(trimmed)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func uniqueChannelGroups(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := internalrouting.NormalizeGroupName(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func addKnownChannel(known map[string]string, rawName, source string) error {
	name := strings.TrimSpace(rawName)
	if name == "" {
		return nil
	}
	key := strings.ToLower(name)
	if existing, exists := known[key]; exists && existing != source {
		return fmt.Errorf("channel name %q is already used by %s", name, existing)
	}
	known[key] = source
	return nil
}

func collectKnownChannels(cfg *config.Config, auths []*coreauth.Auth, excludeAuthID string) (map[string]string, error) {
	known := make(map[string]string)
	if cfg != nil {
		for _, entry := range cfg.GeminiKey {
			if err := addKnownChannel(known, entry.Name, "Gemini API key config"); err != nil {
				return nil, err
			}
		}
		for _, entry := range cfg.ClaudeKey {
			if err := addKnownChannel(known, entry.Name, "Claude API key config"); err != nil {
				return nil, err
			}
		}
		for _, entry := range cfg.CodexKey {
			if err := addKnownChannel(known, entry.Name, "Codex API key config"); err != nil {
				return nil, err
			}
		}
		for _, entry := range cfg.OpenAICompatibility {
			if err := addKnownChannel(known, entry.Name, "OpenAI compatibility config"); err != nil {
				return nil, err
			}
		}
	}

	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if excludeAuthID != "" && strings.EqualFold(strings.TrimSpace(auth.ID), strings.TrimSpace(excludeAuthID)) {
			continue
		}
		accountType, _ := auth.AccountInfo()
		if !strings.EqualFold(accountType, "oauth") {
			continue
		}
		channel := auth.ChannelName()
		if err := addKnownChannel(known, channel, "OAuth auth file"); err != nil {
			return nil, err
		}
	}

	return known, nil
}

func (h *Handler) validateChannelNames() error {
	var auths []*coreauth.Auth
	if h != nil && h.authManager != nil {
		auths = h.authManager.List()
	}
	_, err := collectKnownChannels(h.cfg, auths, "")
	return err
}

func (h *Handler) validateAllowedChannels(values []string) ([]string, error) {
	normalized := uniqueChannels(values)
	if len(normalized) == 0 {
		return nil, nil
	}
	var auths []*coreauth.Auth
	if h != nil && h.authManager != nil {
		auths = h.authManager.List()
	}
	known, err := collectKnownChannels(h.cfg, auths, "")
	if err != nil {
		return nil, err
	}
	for _, value := range normalized {
		key := strings.ToLower(strings.TrimSpace(value))
		if _, exists := known[key]; !exists {
			return nil, fmt.Errorf("unknown channel %q", value)
		}
	}
	return normalized, nil
}

func (h *Handler) validateAllowedChannelGroups(values []string) ([]string, error) {
	normalized := uniqueChannelGroups(values)
	if len(normalized) == 0 {
		return nil, nil
	}
	if h == nil || h.authManager == nil {
		return normalized, nil
	}
	known := h.authManager.KnownChannelGroups()
	for _, value := range normalized {
		if _, exists := known[value]; !exists {
			return nil, fmt.Errorf("unknown channel group %q", value)
		}
	}
	return normalized, nil
}

func (h *Handler) validateAuthChannelName(name, excludeAuthID string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("channel name is required")
	}
	var auths []*coreauth.Auth
	if h != nil && h.authManager != nil {
		auths = h.authManager.List()
	}
	known, err := collectKnownChannels(h.cfg, auths, excludeAuthID)
	if err != nil {
		return "", err
	}
	key := strings.ToLower(trimmed)
	if existing, exists := known[key]; exists {
		return "", fmt.Errorf("channel name %q is already used by %s", trimmed, existing)
	}
	return trimmed, nil
}
