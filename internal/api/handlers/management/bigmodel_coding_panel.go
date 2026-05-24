package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	bigModelCodingProviderName = config.DefaultBigModelCodingProviderName
	bigModelCodingBaseURL      = config.DefaultBigModelCodingBaseURL
	bigModelCodingModel        = config.DefaultBigModelCodingModel
	bigModelCodingAlias        = config.DefaultBigModelCodingAlias
)

func (h *Handler) GetBigModelCodingKeys(c *gin.Context) {
	items := h.bigModelCodingWithAuthIndex()
	c.JSON(http.StatusOK, gin.H{
		"bigmodel-coding":         items,
		"bigmodel-coding-api-key": items,
	})
}

func (h *Handler) PutBigModelCodingKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	entries, ok := decodeBigModelCodingPayload(data)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	for i := range entries {
		normalizeBigModelCodingEntry(&entries[i])
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.MigrateBigModelCodingFromOpenAICompatibility()
	h.cfg.BigModelCodingAPIKey = append([]config.OpenAICompatibility(nil), entries...)
	h.cfg.SanitizeBigModelCoding()
	h.persistLocked(c)
}

func (h *Handler) PatchBigModelCodingKey(c *gin.Context) {
	type bigModelCodingPatch struct {
		Prefix              *string                             `json:"prefix"`
		Priority            *int                                `json:"priority"`
		Disabled            *bool                               `json:"disabled"`
		BaseURL             *string                             `json:"base-url"`
		TestModel           *string                             `json:"test-model"`
		APIKeyEntries       *[]config.OpenAICompatibilityAPIKey `json:"api-key-entries"`
		Models              *[]config.OpenAICompatibilityModel  `json:"models"`
		Headers             *map[string]string                  `json:"headers"`
		IdentityFingerprint *string                             `json:"identity-fingerprint"`
		DisableCooling      *bool                               `json:"disable-cooling"`
	}
	var body struct {
		Value *bigModelCodingPatch `json:"value"`
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
		body.Value = &bigModelCodingPatch{}
		if err := json.Unmarshal(data, body.Value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.MigrateBigModelCodingFromOpenAICompatibility()
	h.cfg.SanitizeBigModelCoding()
	targetIndex := h.bigModelCodingIndexLocked()
	entry := defaultBigModelCodingEntry()
	if targetIndex >= 0 {
		entry = h.cfg.BigModelCodingAPIKey[targetIndex]
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
	if body.Value.BaseURL != nil {
		entry.BaseURL = strings.TrimSpace(*body.Value.BaseURL)
	}
	if body.Value.TestModel != nil {
		entry.TestModel = strings.TrimSpace(*body.Value.TestModel)
	}
	if body.Value.APIKeyEntries != nil {
		entry.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), (*body.Value.APIKeyEntries)...)
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
		entry.DisableCooling = *body.Value.DisableCooling
	}
	normalizeBigModelCodingEntry(&entry)
	if targetIndex >= 0 {
		h.cfg.BigModelCodingAPIKey[targetIndex] = entry
	} else {
		h.cfg.BigModelCodingAPIKey = append(h.cfg.BigModelCodingAPIKey, entry)
	}
	h.cfg.MigrateBigModelCodingFromOpenAICompatibility()
	h.cfg.SanitizeBigModelCoding()
	h.persistLocked(c)
}

func (h *Handler) DeleteBigModelCodingKey(c *gin.Context) {
	apiKey := strings.TrimSpace(c.Query("api-key"))
	name := strings.TrimSpace(c.Query("name"))
	indexStr := strings.TrimSpace(c.Query("index"))

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.MigrateBigModelCodingFromOpenAICompatibility()
	h.cfg.SanitizeBigModelCoding()
	if indexStr != "" {
		var idx int
		if _, err := fmt.Sscanf(indexStr, "%d", &idx); err == nil && idx >= 0 && idx < len(h.cfg.BigModelCodingAPIKey) {
			h.cfg.BigModelCodingAPIKey = append(h.cfg.BigModelCodingAPIKey[:idx], h.cfg.BigModelCodingAPIKey[idx+1:]...)
			h.cfg.SanitizeBigModelCoding()
			h.persistLocked(c)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
		return
	}
	if name != "" {
		out := make([]config.OpenAICompatibility, 0, len(h.cfg.BigModelCodingAPIKey))
		for _, v := range h.cfg.BigModelCodingAPIKey {
			if strings.TrimSpace(v.Name) != name {
				out = append(out, v)
			}
		}
		h.cfg.BigModelCodingAPIKey = out
		h.cfg.SanitizeBigModelCoding()
		h.persistLocked(c)
		return
	}
	if apiKey != "" {
		targetIndex := h.bigModelCodingIndexLocked()
		if targetIndex < 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
			return
		}
		entry := h.cfg.BigModelCodingAPIKey[targetIndex]
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
		normalizeBigModelCodingEntry(&entry)
		h.cfg.BigModelCodingAPIKey[targetIndex] = entry
		h.cfg.SanitizeBigModelCoding()
		h.persistLocked(c)
		return
	}

	h.cfg.BigModelCodingAPIKey = nil
	h.persistLocked(c)
}

func (h *Handler) bigModelCodingWithAuthIndex() []openAICompatibilityWithAuthIndex {
	return h.openAICompatibilityEntriesWithAuthIndex(h.bigModelCodingEntriesLocked, "bigmodel-coding")
}

func (h *Handler) bigModelCodingIndexLocked() int {
	if h == nil || h.cfg == nil {
		return -1
	}
	h.cfg.MigrateBigModelCodingFromOpenAICompatibility()
	h.cfg.SanitizeBigModelCoding()
	for i := range h.cfg.BigModelCodingAPIKey {
		if isBigModelCodingEntry(h.cfg.BigModelCodingAPIKey[i]) {
			return i
		}
	}
	return -1
}

func decodeBigModelCodingPayload(data []byte) ([]config.OpenAICompatibility, bool) {
	var arr []config.OpenAICompatibility
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr, true
	}

	var wrapped struct {
		BigModelCoding       []config.OpenAICompatibility       `json:"bigmodel-coding"`
		BigModelCodingLegacy []config.OpenAICompatibility       `json:"bigmodel-coding-api-key"`
		Items                []config.OpenAICompatibility       `json:"items"`
		APIKeyEntries        []config.OpenAICompatibilityAPIKey `json:"api-key-entries"`
		APIKey               string                             `json:"api-key"`
		BaseURL              string                             `json:"base-url"`
		Models               []config.OpenAICompatibilityModel  `json:"models"`
		Headers              map[string]string                  `json:"headers"`
		Disabled             bool                               `json:"disabled"`
		Priority             int                                `json:"priority"`
		Prefix               string                             `json:"prefix"`
		TestModel            string                             `json:"test-model"`
		DisableCooling       bool                               `json:"disable-cooling"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, false
	}
	switch {
	case wrapped.BigModelCoding != nil:
		return wrapped.BigModelCoding, true
	case wrapped.BigModelCodingLegacy != nil:
		return wrapped.BigModelCodingLegacy, true
	case wrapped.Items != nil:
		return wrapped.Items, true
	default:
		entry := defaultBigModelCodingEntry()
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
		entry.DisableCooling = wrapped.DisableCooling
		return []config.OpenAICompatibility{entry}, true
	}
}

func defaultBigModelCodingEntry() config.OpenAICompatibility {
	return config.OpenAICompatibility{
		Name:                bigModelCodingProviderName,
		BaseURL:             bigModelCodingBaseURL,
		TestModel:           bigModelCodingModel,
		IdentityFingerprint: "codex",
		Models: []config.OpenAICompatibilityModel{
			{Name: bigModelCodingModel, Alias: bigModelCodingAlias},
		},
	}
}

func normalizeBigModelCodingEntry(entry *config.OpenAICompatibility) {
	if entry == nil {
		return
	}
	entry.Name = bigModelCodingProviderName
	if strings.TrimSpace(entry.BaseURL) == "" {
		entry.BaseURL = bigModelCodingBaseURL
	}
	if strings.TrimSpace(entry.TestModel) == "" {
		entry.TestModel = bigModelCodingModel
	}
	entry.Prefix = strings.TrimSpace(entry.Prefix)
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	entry.TestModel = strings.TrimSpace(entry.TestModel)
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	entry.IdentityFingerprint = "codex"
	ensureBigModelCodingAlias(entry)
}

func ensureBigModelCodingAlias(entry *config.OpenAICompatibility) {
	for i := range entry.Models {
		entry.Models[i].Name = strings.TrimSpace(entry.Models[i].Name)
		entry.Models[i].Alias = strings.TrimSpace(entry.Models[i].Alias)
		if entry.Models[i].Name == bigModelCodingModel && entry.Models[i].Alias == bigModelCodingAlias {
			return
		}
	}
	entry.Models = append(entry.Models, config.OpenAICompatibilityModel{
		Name:  bigModelCodingModel,
		Alias: bigModelCodingAlias,
	})
}

func isBigModelCodingEntry(entry config.OpenAICompatibility) bool {
	return strings.EqualFold(strings.TrimSpace(entry.Name), bigModelCodingProviderName)
}
