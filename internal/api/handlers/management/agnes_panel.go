package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func (h *Handler) GetAgnesKeys(c *gin.Context) {
	items := h.agnesWithAuthIndex()
	c.JSON(http.StatusOK, gin.H{
		"agnes":         items,
		"agnes-api-key": items,
	})
}

func (h *Handler) PutAgnesKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	entries, ok := decodeAgnesPayload(data)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	for i := range entries {
		normalizeAgnesEntry(&entries[i])
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.MigrateAgnesFromOpenAICompatibility()
	entries, err = restoreMaskedOpenAICompatibilityKeys(entries, h.cfg.AgnesAPIKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.cfg.AgnesAPIKey = append([]config.OpenAICompatibility(nil), entries...)
	h.cfg.SanitizeAgnes()
	h.persistLocked(c)
}

func (h *Handler) PatchAgnesKey(c *gin.Context) {
	type agnesPatch struct {
		Prefix              *string                             `json:"prefix"`
		Priority            *int                                `json:"priority"`
		Disabled            *bool                               `json:"disabled"`
		BillingMultiplier   *float64                            `json:"billing-multiplier"`
		BaseURL             *string                             `json:"base-url"`
		TestModel           *string                             `json:"test-model"`
		APIKeyEntries       *[]config.OpenAICompatibilityAPIKey `json:"api-key-entries"`
		Models              *[]config.OpenAICompatibilityModel  `json:"models"`
		Headers             *map[string]string                  `json:"headers"`
		IdentityFingerprint *string                             `json:"identity-fingerprint"`
		DisableCooling      *bool                               `json:"disable-cooling"`
		ResponseEndpoint    *bool                               `json:"response-endpoint"`
	}
	var body struct {
		Index *int        `json:"index"`
		Value *agnesPatch `json:"value"`
	}
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	if err := json.Unmarshal(data, &body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if body.Value == nil {
		body.Value = &agnesPatch{}
		if err := json.Unmarshal(data, body.Value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.MigrateAgnesFromOpenAICompatibility()
	h.cfg.SanitizeAgnes()
	targetIndex := h.agnesIndexLocked()
	if body.Index != nil {
		if *body.Index < 0 || *body.Index >= len(h.cfg.AgnesAPIKey) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
			return
		}
		targetIndex = *body.Index
	}
	if indexStr := strings.TrimSpace(c.Query("index")); indexStr != "" {
		var index int
		if _, err := fmt.Sscanf(indexStr, "%d", &index); err != nil || index < 0 || index >= len(h.cfg.AgnesAPIKey) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
			return
		}
		targetIndex = index
	}
	entry := defaultAgnesEntry()
	if targetIndex >= 0 {
		entry = h.cfg.AgnesAPIKey[targetIndex]
	}
	if body.Value.Prefix != nil {
		entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
	}
	if body.Value.Priority != nil {
		entry.Priority = *body.Value.Priority
	}
	if body.Value.Disabled != nil {
		entry.Disabled = *body.Value.Disabled
	}
	if body.Value.BillingMultiplier != nil {
		entry.BillingMultiplier = *body.Value.BillingMultiplier
	}
	if body.Value.BaseURL != nil {
		entry.BaseURL = strings.TrimSpace(*body.Value.BaseURL)
	}
	if body.Value.TestModel != nil {
		entry.TestModel = strings.TrimSpace(*body.Value.TestModel)
	}
	if body.Value.APIKeyEntries != nil {
		entry.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), (*body.Value.APIKeyEntries)...)
		existingEntries := []config.OpenAICompatibility(nil)
		if targetIndex >= 0 {
			existingEntries = []config.OpenAICompatibility{h.cfg.AgnesAPIKey[targetIndex]}
		}
		restored, err := restoreMaskedOpenAICompatibilityKeys([]config.OpenAICompatibility{entry}, existingEntries)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		entry.APIKeyEntries = restored[0].APIKeyEntries
	}
	if body.Value.Models != nil {
		entry.Models = append([]config.OpenAICompatibilityModel(nil), (*body.Value.Models)...)
	}
	if body.Value.Headers != nil {
		entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
	}
	if body.Value.IdentityFingerprint != nil {
		entry.IdentityFingerprint = strings.TrimSpace(*body.Value.IdentityFingerprint)
	}
	if body.Value.DisableCooling != nil {
		entry.DisableCooling = cloneOptionalBool(body.Value.DisableCooling)
	}
	if body.Value.ResponseEndpoint != nil {
		entry.ResponseEndpoint = *body.Value.ResponseEndpoint
	}
	normalizeAgnesEntry(&entry)
	if targetIndex >= 0 {
		h.cfg.AgnesAPIKey[targetIndex] = entry
	} else {
		h.cfg.AgnesAPIKey = append(h.cfg.AgnesAPIKey, entry)
	}
	h.cfg.SanitizeAgnes()
	h.persistLocked(c)
}

func (h *Handler) DeleteAgnesKey(c *gin.Context) {
	apiKey := strings.TrimSpace(c.Query("api-key"))
	name := strings.TrimSpace(c.Query("name"))
	indexStr := strings.TrimSpace(c.Query("index"))

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.MigrateAgnesFromOpenAICompatibility()
	h.cfg.SanitizeAgnes()
	if indexStr != "" {
		var idx int
		if _, err := fmt.Sscanf(indexStr, "%d", &idx); err == nil && idx >= 0 && idx < len(h.cfg.AgnesAPIKey) {
			h.cfg.AgnesAPIKey = append(h.cfg.AgnesAPIKey[:idx], h.cfg.AgnesAPIKey[idx+1:]...)
			h.cfg.SanitizeAgnes()
			h.persistLocked(c)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
		return
	}
	if name != "" {
		out := make([]config.OpenAICompatibility, 0, len(h.cfg.AgnesAPIKey))
		for _, entry := range h.cfg.AgnesAPIKey {
			if !strings.EqualFold(strings.TrimSpace(entry.Name), name) {
				out = append(out, entry)
			}
		}
		h.cfg.AgnesAPIKey = out
		h.cfg.SanitizeAgnes()
		h.persistLocked(c)
		return
	}
	if apiKey != "" {
		targetIndex := h.agnesIndexLocked()
		if targetIndex < 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
			return
		}
		entry := h.cfg.AgnesAPIKey[targetIndex]
		nextKeys := make([]config.OpenAICompatibilityAPIKey, 0, len(entry.APIKeyEntries))
		for _, keyEntry := range entry.APIKeyEntries {
			if strings.TrimSpace(keyEntry.APIKey) != apiKey {
				nextKeys = append(nextKeys, keyEntry)
			}
		}
		if len(nextKeys) == len(entry.APIKeyEntries) {
			c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
			return
		}
		entry.APIKeyEntries = nextKeys
		normalizeAgnesEntry(&entry)
		h.cfg.AgnesAPIKey[targetIndex] = entry
		h.cfg.SanitizeAgnes()
		h.persistLocked(c)
		return
	}

	c.JSON(http.StatusBadRequest, gin.H{"error": "missing api-key, name, or index"})
}

func (h *Handler) agnesWithAuthIndex() []openAICompatibilityWithAuthIndex {
	return h.openAICompatibilityEntriesWithAuthIndex(h.agnesEntriesLocked, "agnes")
}

func (h *Handler) agnesIndexLocked() int {
	if h == nil || h.cfg == nil {
		return -1
	}
	h.cfg.MigrateAgnesFromOpenAICompatibility()
	h.cfg.SanitizeAgnes()
	if len(h.cfg.AgnesAPIKey) == 0 {
		return -1
	}
	return 0
}

func decodeAgnesPayload(data []byte) ([]config.OpenAICompatibility, bool) {
	var entries []config.OpenAICompatibility
	if err := json.Unmarshal(data, &entries); err == nil {
		return entries, true
	}

	var wrapped struct {
		Agnes            []config.OpenAICompatibility       `json:"agnes"`
		AgnesLegacy      []config.OpenAICompatibility       `json:"agnes-api-key"`
		Items            []config.OpenAICompatibility       `json:"items"`
		APIKeyEntries    []config.OpenAICompatibilityAPIKey `json:"api-key-entries"`
		APIKey           string                             `json:"api-key"`
		BaseURL          string                             `json:"base-url"`
		Models           []config.OpenAICompatibilityModel  `json:"models"`
		Headers          map[string]string                  `json:"headers"`
		Disabled         bool                               `json:"disabled"`
		Priority         int                                `json:"priority"`
		Prefix           string                             `json:"prefix"`
		TestModel        string                             `json:"test-model"`
		DisableCooling   *bool                              `json:"disable-cooling"`
		ResponseEndpoint bool                               `json:"response-endpoint"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, false
	}
	switch {
	case wrapped.Agnes != nil:
		return wrapped.Agnes, true
	case wrapped.AgnesLegacy != nil:
		return wrapped.AgnesLegacy, true
	case wrapped.Items != nil:
		return wrapped.Items, true
	default:
		entry := defaultAgnesEntry()
		entry.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), wrapped.APIKeyEntries...)
		if key := strings.TrimSpace(wrapped.APIKey); key != "" {
			entry.APIKeyEntries = append(entry.APIKeyEntries, config.OpenAICompatibilityAPIKey{APIKey: key})
		}
		entry.BaseURL = wrapped.BaseURL
		entry.Models = append([]config.OpenAICompatibilityModel(nil), wrapped.Models...)
		entry.Headers = config.NormalizeHeaders(wrapped.Headers)
		entry.Disabled = wrapped.Disabled
		entry.Priority = wrapped.Priority
		entry.Prefix = strings.TrimSpace(wrapped.Prefix)
		entry.TestModel = strings.TrimSpace(wrapped.TestModel)
		entry.DisableCooling = cloneOptionalBool(wrapped.DisableCooling)
		entry.ResponseEndpoint = wrapped.ResponseEndpoint
		return []config.OpenAICompatibility{entry}, true
	}
}

func defaultAgnesEntry() config.OpenAICompatibility {
	return config.OpenAICompatibility{
		Name:      config.DefaultAgnesProviderName,
		BaseURL:   config.DefaultAgnesBaseURL,
		TestModel: config.DefaultAgnesChatModel,
	}
}

func normalizeAgnesEntry(entry *config.OpenAICompatibility) {
	if entry == nil {
		return
	}
	entry.Name = config.DefaultAgnesProviderName
	entry.Prefix = strings.TrimSpace(entry.Prefix)
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	if entry.BaseURL == "" {
		entry.BaseURL = config.DefaultAgnesBaseURL
	}
	entry.TestModel = strings.TrimSpace(entry.TestModel)
	if entry.TestModel == "" {
		entry.TestModel = config.DefaultAgnesChatModel
	}
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	for i := range entry.Models {
		entry.Models[i].Name = strings.TrimSpace(entry.Models[i].Name)
		entry.Models[i].Alias = strings.TrimSpace(entry.Models[i].Alias)
	}
}

func isAgnesManagementEntry(entry config.OpenAICompatibility) bool {
	name := strings.ToLower(strings.TrimSpace(entry.Name))
	if name == config.DefaultAgnesProviderName || name == "agnes-ai" {
		return true
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(entry.BaseURL)), "agnes-ai.com")
}

func restoreMaskedOpenAICompatibilityKeys(incoming, existing []config.OpenAICompatibility) ([]config.OpenAICompatibility, error) {
	if len(incoming) == 0 {
		return incoming, nil
	}
	out := append([]config.OpenAICompatibility(nil), incoming...)
	for i := range out {
		if i >= len(existing) {
			if hasMaskedOpenAICompatibilityKey(out[i].APIKeyEntries) {
				return nil, fmt.Errorf("masked api key cannot be matched; reload the page or enter the full key")
			}
			continue
		}
		for j := range out[i].APIKeyEntries {
			key := strings.TrimSpace(out[i].APIKeyEntries[j].APIKey)
			if !isMaskedAPIKey(key) {
				continue
			}
			match := ""
			if j < len(existing[i].APIKeyEntries) {
				candidate := strings.TrimSpace(existing[i].APIKeyEntries[j].APIKey)
				if maskAPIKey(candidate) == key {
					match = candidate
				}
			}
			if match == "" {
				for _, candidateEntry := range existing {
					for _, candidateKey := range candidateEntry.APIKeyEntries {
						candidate := strings.TrimSpace(candidateKey.APIKey)
						if maskAPIKey(candidate) != key {
							continue
						}
						if match != "" {
							return nil, fmt.Errorf("masked api key matches multiple configured keys")
						}
						match = candidate
					}
				}
			}
			if match == "" {
				return nil, fmt.Errorf("masked api key cannot be matched; reload the page or enter the full key")
			}
			out[i].APIKeyEntries[j].APIKey = match
		}
	}
	return out, nil
}

func hasMaskedOpenAICompatibilityKey(entries []config.OpenAICompatibilityAPIKey) bool {
	for _, entry := range entries {
		if isMaskedAPIKey(entry.APIKey) {
			return true
		}
	}
	return false
}
