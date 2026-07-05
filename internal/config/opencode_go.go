package config

import "strings"

type OpenCodeGoKey struct {
	Name              string            `yaml:"name,omitempty" json:"name,omitempty"`
	Priority          int               `yaml:"priority,omitempty" json:"priority,omitempty"`
	Prefix            string            `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	APIKey            string            `yaml:"api-key,omitempty" json:"api-key,omitempty"`
	BaseURL           string            `yaml:"base-url,omitempty" json:"base-url,omitempty"`
	ProxyURL          string            `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
	ProxyID           string            `yaml:"proxy-id,omitempty" json:"proxy-id,omitempty"`
	VisionModel       string            `yaml:"vision-model,omitempty" json:"vision-model,omitempty"`
	Models            []OpenCodeGoModel `yaml:"models,omitempty" json:"models,omitempty"`
	Headers           map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	ExcludedModels    []string          `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`
	BillingMultiplier float64           `yaml:"billing-multiplier,omitempty" json:"billing-multiplier,omitempty"`
}

type OpenCodeGoModel struct {
	Name  string `yaml:"name" json:"name"`
	Alias string `yaml:"alias" json:"alias"`
}

func (m OpenCodeGoModel) GetName() string  { return m.Name }
func (m OpenCodeGoModel) GetAlias() string { return m.Alias }

func (cfg *Config) SanitizeOpenCodeGoKeys() {
	if cfg == nil {
		return
	}
	seen := make(map[string]struct{}, len(cfg.OpenCodeGoKey))
	out := cfg.OpenCodeGoKey[:0]
	for i := range cfg.OpenCodeGoKey {
		entry := cfg.OpenCodeGoKey[i]
		entry.Name = strings.TrimSpace(entry.Name)
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.ProxyID = strings.TrimSpace(entry.ProxyID)
		entry.VisionModel = strings.TrimSpace(entry.VisionModel)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
		entry.Models = sanitizeOpenCodeGoModels(entry.Models)

		if entry.APIKey == "" {
			continue
		}
		credentialKey := strings.Join([]string{entry.APIKey, entry.BaseURL}, "|")
		if _, exists := seen[credentialKey]; exists {
			continue
		}
		seen[credentialKey] = struct{}{}
		out = append(out, entry)
	}
	cfg.OpenCodeGoKey = out
}

func sanitizeOpenCodeGoModels(models []OpenCodeGoModel) []OpenCodeGoModel {
	if len(models) == 0 {
		return nil
	}
	out := make([]OpenCodeGoModel, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model.Name = strings.TrimSpace(model.Name)
		model.Alias = strings.TrimSpace(model.Alias)
		if model.Name == "" && model.Alias == "" {
			continue
		}
		key := strings.ToLower(model.Name) + "|" + strings.ToLower(model.Alias)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
