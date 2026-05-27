package management

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalrouting "github.com/router-for-me/CLIProxyAPI/v7/internal/routing"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func normalizeChannelName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

type knownChannel struct {
	Canonical string
	Source    string
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

func addKnownChannel(known map[string]knownChannel, rawName, canonicalName, source string) error {
	return addKnownChannelWithPolicy(known, rawName, canonicalName, source, true)
}

func addKnownChannelWithPolicy(known map[string]knownChannel, rawName, canonicalName, source string, failOnConflict bool) error {
	name := strings.TrimSpace(rawName)
	canonicalName = strings.TrimSpace(canonicalName)
	if name == "" || canonicalName == "" {
		return nil
	}
	key := strings.ToLower(name)
	if existing, exists := known[key]; exists && !strings.EqualFold(existing.Canonical, canonicalName) {
		if !failOnConflict {
			return nil
		}
		return fmt.Errorf("channel name %q is already used by %s", name, existing.Source)
	}
	known[key] = knownChannel{Canonical: canonicalName, Source: source}
	return nil
}

func collectKnownChannels(cfg *config.Config, auths []*coreauth.Auth, excludeAuthID string) (map[string]knownChannel, error) {
	return collectKnownChannelsWithPolicy(cfg, auths, excludeAuthID, true)
}

func collectKnownChannelsForAuthRename(cfg *config.Config, auths []*coreauth.Auth, excludeAuthID string) map[string]knownChannel {
	known, _ := collectKnownChannelsWithPolicy(cfg, auths, excludeAuthID, false)
	return known
}

func collectKnownChannelsWithPolicy(cfg *config.Config, auths []*coreauth.Auth, excludeAuthID string, failOnConflict bool) (map[string]knownChannel, error) {
	known := make(map[string]knownChannel)
	if cfg != nil {
		for _, entry := range cfg.GeminiKey {
			if strings.TrimSpace(entry.APIKey) == "" {
				continue
			}
			name := entry.Prefix
			if name == "" {
				name = "gemini"
			}
			if err := addKnownChannelWithPolicy(known, name, name, "Gemini API key config", failOnConflict); err != nil {
				return nil, err
			}
		}
		for _, entry := range cfg.ClaudeKey {
			if strings.TrimSpace(entry.APIKey) == "" {
				continue
			}
			name := entry.Prefix
			if name == "" {
				name = "claude"
			}
			if err := addKnownChannelWithPolicy(known, name, name, "Claude API key config", failOnConflict); err != nil {
				return nil, err
			}
		}
		for _, entry := range cfg.CodexKey {
			if strings.TrimSpace(entry.APIKey) == "" {
				continue
			}
			name := entry.Prefix
			if name == "" {
				name = "codex"
			}
			if err := addKnownChannelWithPolicy(known, name, name, "Codex API key config", failOnConflict); err != nil {
				return nil, err
			}
		}
		for _, entry := range cfg.OpenAICompatibility {
			if strings.TrimSpace(entry.BaseURL) == "" {
				continue
			}
			if err := addKnownChannelWithPolicy(known, entry.Name, entry.Name, "OpenAI compatibility config", failOnConflict); err != nil {
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
		canonical := auth.ChannelName()
		if err := addKnownChannelWithPolicy(known, canonical, canonical, "OAuth auth file", failOnConflict); err != nil {
			return nil, err
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
		canonical := strings.TrimSpace(auth.ChannelName())
		for _, identifier := range auth.ChannelIdentifiers() {
			if strings.EqualFold(strings.TrimSpace(identifier), canonical) {
				continue
			}
			if err := addKnownChannelWithPolicy(known, identifier, canonical, "OAuth auth file", false); err != nil {
				return nil, err
			}
		}
	}

	return known, nil
}

func canonicalChannelName(value string, known map[string]knownChannel) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if entry, exists := known[strings.ToLower(value)]; exists && strings.TrimSpace(entry.Canonical) != "" {
		return strings.TrimSpace(entry.Canonical)
	}
	return value
}

func canonicalizeChannelList(values []string, known map[string]knownChannel) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		canonical := canonicalChannelName(value, known)
		if canonical == "" {
			continue
		}
		key := strings.ToLower(canonical)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, canonical)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func canonicalizeChannelPriorities(values map[string]int, known map[string]knownChannel) map[string]int {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]int, len(values))
	for name, priority := range values {
		canonical := canonicalChannelName(name, known)
		if canonical == "" || priority < 0 {
			continue
		}
		if existing, exists := out[canonical]; !exists || priority > existing {
			out[canonical] = priority
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func canonicalizeRoutingConfigChannels(routing config.RoutingConfig, known map[string]knownChannel) config.RoutingConfig {
	if len(routing.ChannelGroups) == 0 {
		return routing
	}
	routing.ChannelGroups = append([]config.RoutingChannelGroup(nil), routing.ChannelGroups...)
	for i := range routing.ChannelGroups {
		group := routing.ChannelGroups[i]
		group.Match.Channels = canonicalizeChannelList(group.Match.Channels, known)
		group.ChannelPriorities = canonicalizeChannelPriorities(group.ChannelPriorities, known)
		routing.ChannelGroups[i] = group
	}
	return routing
}

func (h *Handler) validateChannelNames() error {
	var auths []*coreauth.Auth
	if h != nil && h.authManager != nil {
		auths = h.authManager.List()
	}
	_, err := collectKnownChannels(h.cfg, auths, "")
	return err
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
