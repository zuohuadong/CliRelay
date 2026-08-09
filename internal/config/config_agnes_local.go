package config

import "strings"

func (cfg *Config) MigrateAgnesFromOpenAICompatibility() {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return
	}
	nextCompat := make([]OpenAICompatibility, 0, len(cfg.OpenAICompatibility))
	for i := range cfg.OpenAICompatibility {
		entry := cfg.OpenAICompatibility[i]
		if isAgnesOpenAICompatibilityEntry(entry) {
			cfg.AgnesAPIKey = append(cfg.AgnesAPIKey, entry)
			continue
		}
		nextCompat = append(nextCompat, entry)
	}
	cfg.OpenAICompatibility = nextCompat
}

func (cfg *Config) SanitizeAgnes() {
	if cfg == nil || len(cfg.AgnesAPIKey) == 0 {
		return
	}
	out := make([]OpenAICompatibility, 0, len(cfg.AgnesAPIKey))
	for i := range cfg.AgnesAPIKey {
		entry := cfg.AgnesAPIKey[i]
		entry.Name = DefaultAgnesProviderName
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		if entry.BaseURL == "" {
			entry.BaseURL = DefaultAgnesBaseURL
		}
		entry.TestModel = strings.TrimSpace(entry.TestModel)
		if entry.TestModel == "" {
			entry.TestModel = DefaultAgnesChatModel
		}
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.IdentityFingerprint = strings.ToLower(strings.TrimSpace(entry.IdentityFingerprint))
		entry.Models = ensureAgnesModels(entry.Models)
		out = append(out, entry)
	}
	cfg.AgnesAPIKey = out
}

func isAgnesOpenAICompatibilityEntry(entry OpenAICompatibility) bool {
	name := strings.ToLower(strings.TrimSpace(entry.Name))
	if name == DefaultAgnesProviderName || name == "agnes-ai" {
		return true
	}
	baseURL := strings.ToLower(strings.TrimSpace(entry.BaseURL))
	return strings.Contains(baseURL, "agnes-ai.com")
}

func ensureAgnesModels(models []OpenAICompatibilityModel) []OpenAICompatibilityModel {
	if len(models) == 0 {
		return nil
	}
	for i := range models {
		models[i].Name = strings.TrimSpace(models[i].Name)
		models[i].Alias = strings.TrimSpace(models[i].Alias)
		modelID := models[i].Name
		if modelID == "" {
			modelID = models[i].Alias
			models[i].Name = modelID
		}
		if models[i].Alias == "" {
			models[i].Alias = modelID
		}
		baseModel := strings.ToLower(strings.TrimSpace(modelID))
		switch {
		case strings.HasPrefix(baseModel, "agnes-image-"):
			models[i].Image = true
		case strings.HasPrefix(baseModel, "agnes-video-"):
			models[i].Video = true
		}
	}
	return models
}
