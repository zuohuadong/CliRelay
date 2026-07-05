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
	astronCodeProviderName = config.DefaultAstronCodeProviderName
	astronCodeBaseURL      = config.DefaultAstronCodeBaseURL
	astronCodeModel        = config.DefaultAstronCodeModel
	astronCodeAlias        = config.DefaultAstronCodeAlias
)

func (h *Handler) GetAstronCodeKeys(c *gin.Context) {
	items := h.astronCodeWithAuthIndex()
	c.JSON(http.StatusOK, gin.H{
		"astron-code":         items,
		"astron-code-api-key": items,
	})
}

func (h *Handler) PutAstronCodeKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	entries, ok := decodeAstronCodePayload(data)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	for i := range entries {
		normalizeAstronCodeEntry(&entries[i])
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.MigrateAstronCodeFromOpenAICompatibility()
	h.cfg.AstronCodeAPIKey = append([]config.OpenAICompatibility(nil), entries...)
	h.cfg.SanitizeAstronCode()
	h.persistLocked(c)
}

func (h *Handler) PatchAstronCodeKey(c *gin.Context) {
	type astronCodePatch struct {
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
		Value *astronCodePatch `json:"value"`
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
		body.Value = &astronCodePatch{}
		if err := json.Unmarshal(data, body.Value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.MigrateAstronCodeFromOpenAICompatibility()
	h.cfg.SanitizeAstronCode()
	targetIndex := h.astronCodeIndexLocked()
	entry := defaultAstronCodeEntry()
	if targetIndex >= 0 {
		entry = h.cfg.AstronCodeAPIKey[targetIndex]
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
	if body.Value.ResponseEndpoint != nil {
		entry.ResponseEndpoint = *body.Value.ResponseEndpoint
	}
	normalizeAstronCodeEntry(&entry)
	if targetIndex >= 0 {
		h.cfg.AstronCodeAPIKey[targetIndex] = entry
	} else {
		h.cfg.AstronCodeAPIKey = append(h.cfg.AstronCodeAPIKey, entry)
	}
	h.cfg.MigrateAstronCodeFromOpenAICompatibility()
	h.cfg.SanitizeAstronCode()
	h.persistLocked(c)
}

func (h *Handler) DeleteAstronCodeKey(c *gin.Context) {
	apiKey := strings.TrimSpace(c.Query("api-key"))
	name := strings.TrimSpace(c.Query("name"))
	indexStr := strings.TrimSpace(c.Query("index"))

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.MigrateAstronCodeFromOpenAICompatibility()
	h.cfg.SanitizeAstronCode()
	if indexStr != "" {
		var idx int
		if _, err := fmt.Sscanf(indexStr, "%d", &idx); err == nil && idx >= 0 && idx < len(h.cfg.AstronCodeAPIKey) {
			h.cfg.AstronCodeAPIKey = append(h.cfg.AstronCodeAPIKey[:idx], h.cfg.AstronCodeAPIKey[idx+1:]...)
			h.cfg.SanitizeAstronCode()
			h.persistLocked(c)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
		return
	}
	if name != "" {
		out := make([]config.OpenAICompatibility, 0, len(h.cfg.AstronCodeAPIKey))
		for _, v := range h.cfg.AstronCodeAPIKey {
			if strings.TrimSpace(v.Name) != name {
				out = append(out, v)
			}
		}
		h.cfg.AstronCodeAPIKey = out
		h.cfg.SanitizeAstronCode()
		h.persistLocked(c)
		return
	}
	if apiKey != "" {
		targetIndex := h.astronCodeIndexLocked()
		if targetIndex < 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
			return
		}
		entry := h.cfg.AstronCodeAPIKey[targetIndex]
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
		normalizeAstronCodeEntry(&entry)
		h.cfg.AstronCodeAPIKey[targetIndex] = entry
		h.cfg.SanitizeAstronCode()
		h.persistLocked(c)
		return
	}

	h.cfg.AstronCodeAPIKey = nil
	h.persistLocked(c)
}

func (h *Handler) astronCodeWithAuthIndex() []openAICompatibilityWithAuthIndex {
	return h.openAICompatibilityEntriesWithAuthIndex(h.astronCodeEntriesLocked, "astron-code")
}

func (h *Handler) astronCodeIndexLocked() int {
	if h == nil || h.cfg == nil {
		return -1
	}
	h.cfg.MigrateAstronCodeFromOpenAICompatibility()
	h.cfg.SanitizeAstronCode()
	for i := range h.cfg.AstronCodeAPIKey {
		if isAstronCodeEntry(h.cfg.AstronCodeAPIKey[i]) {
			return i
		}
	}
	return -1
}

func decodeAstronCodePayload(data []byte) ([]config.OpenAICompatibility, bool) {
	var arr []config.OpenAICompatibility
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr, true
	}

	var wrapped struct {
		AstronCode       []config.OpenAICompatibility       `json:"astron-code"`
		AstronCodeLegacy []config.OpenAICompatibility       `json:"astron-code-api-key"`
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
		DisableCooling   bool                               `json:"disable-cooling"`
		ResponseEndpoint bool                               `json:"response-endpoint"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, false
	}
	switch {
	case wrapped.AstronCode != nil:
		return wrapped.AstronCode, true
	case wrapped.AstronCodeLegacy != nil:
		return wrapped.AstronCodeLegacy, true
	case wrapped.Items != nil:
		return wrapped.Items, true
	default:
		entry := defaultAstronCodeEntry()
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
		entry.ResponseEndpoint = wrapped.ResponseEndpoint
		return []config.OpenAICompatibility{entry}, true
	}
}

func defaultAstronCodeEntry() config.OpenAICompatibility {
	return config.OpenAICompatibility{
		Name:                astronCodeProviderName,
		BaseURL:             astronCodeBaseURL,
		TestModel:           astronCodeModel,
		IdentityFingerprint: "codex",
		Models: []config.OpenAICompatibilityModel{
			{Name: astronCodeModel, Alias: astronCodeAlias},
		},
	}
}

func normalizeAstronCodeEntry(entry *config.OpenAICompatibility) {
	if entry == nil {
		return
	}
	entry.Name = astronCodeProviderName
	if strings.TrimSpace(entry.BaseURL) == "" {
		entry.BaseURL = astronCodeBaseURL
	}
	if strings.TrimSpace(entry.TestModel) == "" {
		entry.TestModel = astronCodeModel
	}
	entry.Prefix = strings.TrimSpace(entry.Prefix)
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	entry.TestModel = strings.TrimSpace(entry.TestModel)
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	entry.IdentityFingerprint = "codex"
	ensureAstronCodeAlias(entry)
}

func ensureAstronCodeAlias(entry *config.OpenAICompatibility) {
	for i := range entry.Models {
		entry.Models[i].Name = strings.TrimSpace(entry.Models[i].Name)
		entry.Models[i].Alias = strings.TrimSpace(entry.Models[i].Alias)
	}
}

func isAstronCodeEntry(entry config.OpenAICompatibility) bool {
	return strings.EqualFold(strings.TrimSpace(entry.Name), astronCodeProviderName)
}
