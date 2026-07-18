package config

import "strings"

const (
	DefaultBedrockRegion  = "us-east-1"
	BedrockAuthModeSigV4  = "sigv4"
	BedrockAuthModeAPIKey = "api-key"
)

type BedrockKey struct {
	Name              string            `yaml:"name,omitempty" json:"name,omitempty"`
	Priority          int               `yaml:"priority,omitempty" json:"priority,omitempty"`
	Prefix            string            `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	AuthMode          string            `yaml:"auth-mode,omitempty" json:"auth-mode,omitempty"`
	APIKey            string            `yaml:"api-key,omitempty" json:"api-key,omitempty"`
	AccessKeyID       string            `yaml:"access-key-id,omitempty" json:"access-key-id,omitempty"`
	SecretAccessKey   string            `yaml:"secret-access-key,omitempty" json:"secret-access-key,omitempty"`
	SessionToken      string            `yaml:"session-token,omitempty" json:"session-token,omitempty"`
	Region            string            `yaml:"region,omitempty" json:"region,omitempty"`
	ForceGlobal       bool              `yaml:"force-global,omitempty" json:"force-global,omitempty"`
	BaseURL           string            `yaml:"base-url,omitempty" json:"base-url,omitempty"`
	ProxyURL          string            `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
	Models            []BedrockModel    `yaml:"models,omitempty" json:"models,omitempty"`
	Headers           map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	ExcludedModels    []string          `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`
	BillingMultiplier float64           `yaml:"billing-multiplier,omitempty" json:"billing-multiplier,omitempty"`
}

func (k BedrockKey) GetAPIKey() string {
	if strings.EqualFold(strings.TrimSpace(k.AuthMode), BedrockAuthModeAPIKey) {
		return k.APIKey
	}
	if strings.TrimSpace(k.APIKey) != "" {
		return k.APIKey
	}
	return k.AccessKeyID
}

func (k BedrockKey) GetBaseURL() string { return k.BaseURL }

type BedrockModel struct {
	Name        string `yaml:"name" json:"name"`
	Alias       string `yaml:"alias" json:"alias"`
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`
}

func (m BedrockModel) GetName() string        { return m.Name }
func (m BedrockModel) GetAlias() string       { return m.Alias }
func (m BedrockModel) GetDisplayName() string { return m.DisplayName }

func (cfg *Config) SanitizeBedrockKeys() {
	if cfg == nil {
		return
	}
	seen := make(map[string]struct{}, len(cfg.BedrockKey))
	out := cfg.BedrockKey[:0]
	for i := range cfg.BedrockKey {
		entry := cfg.BedrockKey[i]
		entry.Name = strings.TrimSpace(entry.Name)
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.AuthMode = normalizeBedrockAuthMode(entry.AuthMode)
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.AccessKeyID = strings.TrimSpace(entry.AccessKeyID)
		entry.SecretAccessKey = strings.TrimSpace(entry.SecretAccessKey)
		entry.SessionToken = strings.TrimSpace(entry.SessionToken)
		entry.Region = strings.TrimSpace(entry.Region)
		if entry.Region == "" {
			entry.Region = DefaultBedrockRegion
		}
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
		entry.Models = sanitizeBedrockModels(entry.Models)

		var credentialKey string
		if entry.AuthMode == BedrockAuthModeAPIKey {
			if entry.APIKey == "" {
				continue
			}
			credentialKey = strings.Join([]string{entry.AuthMode, entry.APIKey, entry.Region, entry.BaseURL}, "|")
		} else {
			if entry.AccessKeyID == "" || entry.SecretAccessKey == "" {
				continue
			}
			credentialKey = strings.Join([]string{entry.AuthMode, entry.AccessKeyID, entry.SecretAccessKey, entry.SessionToken, entry.Region, entry.BaseURL}, "|")
		}
		if _, exists := seen[credentialKey]; exists {
			continue
		}
		seen[credentialKey] = struct{}{}
		out = append(out, entry)
	}
	cfg.BedrockKey = out
}

func normalizeBedrockAuthMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "apikey", "api_key", BedrockAuthModeAPIKey:
		return BedrockAuthModeAPIKey
	default:
		return BedrockAuthModeSigV4
	}
}

func sanitizeBedrockModels(models []BedrockModel) []BedrockModel {
	if len(models) == 0 {
		return nil
	}
	out := make([]BedrockModel, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model.Name = strings.TrimSpace(model.Name)
		model.Alias = strings.TrimSpace(model.Alias)
		model.DisplayName = strings.TrimSpace(model.DisplayName)
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
