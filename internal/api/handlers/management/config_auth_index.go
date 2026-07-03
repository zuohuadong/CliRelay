package management

import (
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type geminiKeyWithAuthIndex struct {
	config.GeminiKey
	AuthIndex      string                         `json:"auth-index,omitempty"`
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests,omitempty"`
}

type claudeKeyWithAuthIndex struct {
	config.ClaudeKey
	AuthIndex      string                         `json:"auth-index,omitempty"`
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests,omitempty"`
}

type codexKeyWithAuthIndex struct {
	config.CodexKey
	AuthIndex      string                         `json:"auth-index,omitempty"`
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests,omitempty"`
}

type vertexCompatKeyWithAuthIndex struct {
	config.VertexCompatKey
	AuthIndex      string                         `json:"auth-index,omitempty"`
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests,omitempty"`
}

type openAICompatibilityAPIKeyWithAuthIndex struct {
	config.OpenAICompatibilityAPIKey
	AuthIndex      string                         `json:"auth-index,omitempty"`
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests,omitempty"`
}

type openAICompatibilityWithAuthIndex struct {
	Name                string                                   `json:"name"`
	Priority            int                                      `json:"priority,omitempty"`
	Disabled            bool                                     `json:"disabled"`
	Prefix              string                                   `json:"prefix,omitempty"`
	BaseURL             string                                   `json:"base-url"`
	TestModel           string                                   `json:"test-model,omitempty"`
	APIKeyEntries       []openAICompatibilityAPIKeyWithAuthIndex `json:"api-key-entries,omitempty"`
	Models              []config.OpenAICompatibilityModel        `json:"models,omitempty"`
	Headers             map[string]string                        `json:"headers,omitempty"`
	IdentityFingerprint string                                   `json:"identity-fingerprint,omitempty"`
	DisableCooling      bool                                     `json:"disable-cooling,omitempty"`
	ResponseEndpoint    bool                                     `json:"response-endpoint,omitempty"`
	AuthIndex           string                                   `json:"auth-index,omitempty"`
	Success             int64                                    `json:"success"`
	Failed              int64                                    `json:"failed"`
	RecentRequests      []coreauth.RecentRequestBucket           `json:"recent_requests,omitempty"`
}

type authUsageSnapshot struct {
	AuthIndex      string
	Success        int64
	Failed         int64
	RecentRequests []coreauth.RecentRequestBucket
}

func cloneRecentRequestBuckets(src []coreauth.RecentRequestBucket) []coreauth.RecentRequestBucket {
	if len(src) == 0 {
		return nil
	}
	return append([]coreauth.RecentRequestBucket(nil), src...)
}

func (h *Handler) liveAuthUsageByID() map[string]authUsageSnapshot {
	out := map[string]authUsageSnapshot{}
	if h == nil {
		return out
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return out
	}
	now := time.Now()
	// authManager.List() returns clones, so EnsureIndex only affects these copies.
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		id := strings.TrimSpace(auth.ID)
		if id == "" {
			continue
		}
		idx := strings.TrimSpace(auth.Index)
		if idx == "" {
			idx = auth.EnsureIndex()
		}
		out[id] = authUsageSnapshot{
			AuthIndex:      idx,
			Success:        auth.Success,
			Failed:         auth.Failed,
			RecentRequests: auth.RecentRequestsSnapshot(now),
		}
	}
	return out
}

func (h *Handler) geminiKeysWithAuthIndex() []geminiKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveUsageByID := h.liveAuthUsageByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]geminiKeyWithAuthIndex, len(h.cfg.GeminiKey))
	for i := range h.cfg.GeminiKey {
		entry := h.cfg.GeminiKey[i]
		usage := authUsageSnapshot{}
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("gemini:apikey", key, entry.BaseURL)
			usage = liveUsageByID[id]
		}
		out[i] = geminiKeyWithAuthIndex{
			GeminiKey:      entry,
			AuthIndex:      usage.AuthIndex,
			Success:        usage.Success,
			Failed:         usage.Failed,
			RecentRequests: cloneRecentRequestBuckets(usage.RecentRequests),
		}
	}
	return out
}

func (h *Handler) claudeKeysWithAuthIndex() []claudeKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveUsageByID := h.liveAuthUsageByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]claudeKeyWithAuthIndex, len(h.cfg.ClaudeKey))
	for i := range h.cfg.ClaudeKey {
		entry := h.cfg.ClaudeKey[i]
		usage := authUsageSnapshot{}
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("claude:apikey", key, entry.BaseURL)
			usage = liveUsageByID[id]
		}
		out[i] = claudeKeyWithAuthIndex{
			ClaudeKey:      entry,
			AuthIndex:      usage.AuthIndex,
			Success:        usage.Success,
			Failed:         usage.Failed,
			RecentRequests: cloneRecentRequestBuckets(usage.RecentRequests),
		}
	}
	return out
}

func (h *Handler) codexKeysWithAuthIndex() []codexKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveUsageByID := h.liveAuthUsageByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]codexKeyWithAuthIndex, len(h.cfg.CodexKey))
	for i := range h.cfg.CodexKey {
		entry := h.cfg.CodexKey[i]
		usage := authUsageSnapshot{}
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("codex:apikey", key, entry.BaseURL)
			usage = liveUsageByID[id]
		}
		out[i] = codexKeyWithAuthIndex{
			CodexKey:       entry,
			AuthIndex:      usage.AuthIndex,
			Success:        usage.Success,
			Failed:         usage.Failed,
			RecentRequests: cloneRecentRequestBuckets(usage.RecentRequests),
		}
	}
	return out
}

func (h *Handler) vertexCompatKeysWithAuthIndex() []vertexCompatKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveUsageByID := h.liveAuthUsageByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]vertexCompatKeyWithAuthIndex, len(h.cfg.VertexCompatAPIKey))
	for i := range h.cfg.VertexCompatAPIKey {
		entry := h.cfg.VertexCompatAPIKey[i]
		id, _ := idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
		usage := liveUsageByID[id]
		out[i] = vertexCompatKeyWithAuthIndex{
			VertexCompatKey: entry,
			AuthIndex:       usage.AuthIndex,
			Success:         usage.Success,
			Failed:          usage.Failed,
			RecentRequests:  cloneRecentRequestBuckets(usage.RecentRequests),
		}
	}
	return out
}

func (h *Handler) openAICompatibilityWithAuthIndex() []openAICompatibilityWithAuthIndex {
	return h.openAICompatibilityEntriesWithAuthIndex(h.openAICompatibilityEntriesLocked, "openai-compatibility")
}

func (h *Handler) openAICompatibilityEntriesWithAuthIndex(entriesFn func() []config.OpenAICompatibility, idPrefix string) []openAICompatibilityWithAuthIndex {
	if h == nil {
		return nil
	}
	liveUsageByID := h.liveAuthUsageByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	entries := []config.OpenAICompatibility(nil)
	if entriesFn != nil {
		entries = entriesFn()
	}
	normalized := normalizedOpenAICompatibilityEntries(entries)
	out := make([]openAICompatibilityWithAuthIndex, len(normalized))
	idGen := synthesizer.NewStableIDGenerator()
	for i := range normalized {
		entry := normalized[i]
		providerName := strings.ToLower(strings.TrimSpace(entry.Name))
		if providerName == "" {
			providerName = strings.TrimSpace(idPrefix)
		}
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		idKindPrefix := strings.TrimSpace(idPrefix)
		if idKindPrefix == "" {
			idKindPrefix = "openai-compatibility"
		}
		idKind := fmt.Sprintf("%s:%s", idKindPrefix, providerName)

		response := openAICompatibilityWithAuthIndex{
			Name:                entry.Name,
			Priority:            entry.Priority,
			Disabled:            entry.Disabled,
			Prefix:              entry.Prefix,
			BaseURL:             entry.BaseURL,
			TestModel:           entry.TestModel,
			Models:              entry.Models,
			Headers:             entry.Headers,
			IdentityFingerprint: entry.IdentityFingerprint,
			DisableCooling:      entry.DisableCooling,
			ResponseEndpoint:    entry.ResponseEndpoint,
			AuthIndex:           "",
		}
		if len(entry.APIKeyEntries) == 0 {
			id, _ := idGen.Next(idKind, entry.BaseURL)
			usage := liveUsageByID[id]
			response.AuthIndex = usage.AuthIndex
			response.Success = usage.Success
			response.Failed = usage.Failed
			response.RecentRequests = cloneRecentRequestBuckets(usage.RecentRequests)
		} else {
			response.APIKeyEntries = make([]openAICompatibilityAPIKeyWithAuthIndex, len(entry.APIKeyEntries))
			for j := range entry.APIKeyEntries {
				apiKeyEntry := entry.APIKeyEntries[j]
				id, _ := idGen.Next(idKind, apiKeyEntry.APIKey, entry.BaseURL, apiKeyEntry.ProxyURL)
				usage := liveUsageByID[id]
				response.APIKeyEntries[j] = openAICompatibilityAPIKeyWithAuthIndex{
					OpenAICompatibilityAPIKey: apiKeyEntry,
					AuthIndex:                 usage.AuthIndex,
					Success:                   usage.Success,
					Failed:                    usage.Failed,
					RecentRequests:            cloneRecentRequestBuckets(usage.RecentRequests),
				}
				response.Success += usage.Success
				response.Failed += usage.Failed
				response.RecentRequests = mergeRecentRequestBuckets(response.RecentRequests, cloneRecentRequestBuckets(usage.RecentRequests))
			}
		}
		out[i] = response
	}
	return out
}

func (h *Handler) openAICompatibilityEntriesLocked() []config.OpenAICompatibility {
	if h == nil || h.cfg == nil {
		return nil
	}
	return append([]config.OpenAICompatibility(nil), h.cfg.OpenAICompatibility...)
}

func (h *Handler) bigModelCodingEntriesLocked() []config.OpenAICompatibility {
	if h == nil || h.cfg == nil {
		return nil
	}
	h.cfg.MigrateBigModelCodingFromOpenAICompatibility()
	h.cfg.SanitizeBigModelCoding()
	return append([]config.OpenAICompatibility(nil), h.cfg.BigModelCodingAPIKey...)
}

func (h *Handler) astronCodeEntriesLocked() []config.OpenAICompatibility {
	if h == nil || h.cfg == nil {
		return nil
	}
	h.cfg.MigrateAstronCodeFromOpenAICompatibility()
	h.cfg.SanitizeAstronCode()
	return append([]config.OpenAICompatibility(nil), h.cfg.AstronCodeAPIKey...)
}

func (h *Handler) iflowWithAuthIndex() []openAICompatibilityWithAuthIndex {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return nil
	}
	liveUsageByID := h.liveAuthUsageByID()

	var entries []openAICompatibilityWithAuthIndex
	for _, auth := range manager.List() {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "iflow") {
			continue
		}
		idx := strings.TrimSpace(auth.Index)
		if idx == "" {
			idx = auth.EnsureIndex()
		}
		id := strings.TrimSpace(auth.ID)
		if id == "" {
			continue
		}
		usage := liveUsageByID[id]
		email := ""
		if auth.Metadata != nil {
			if e, ok := auth.Metadata["email"]; ok {
				if s, ok := e.(string); ok {
					email = strings.TrimSpace(s)
				}
			}
		}
		if email == "" {
			email = strings.TrimSpace(auth.Attributes["api_key"])
		}
		apiKey := strings.TrimSpace(auth.Attributes["api_key"])
		entry := openAICompatibilityWithAuthIndex{
			Name:           "iflow",
			BaseURL:        "https://maas-api.cn-huabei-1.mlf.mlfcore.com",
			AuthIndex:      idx,
			Success:        usage.Success,
			Failed:         usage.Failed,
			RecentRequests: cloneRecentRequestBuckets(usage.RecentRequests),
			APIKeyEntries: []openAICompatibilityAPIKeyWithAuthIndex{
				{
					OpenAICompatibilityAPIKey: config.OpenAICompatibilityAPIKey{
						APIKey: apiKey,
					},
				},
			},
		}
		if email != "" {
			entry.APIKeyEntries[0].APIKey = email
		}
		entries = append(entries, entry)
	}
	return entries
}
