package management

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	configaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	_ "modernc.org/sqlite"
)

type panelAPIKeyEntry struct {
	Key                  string   `json:"key"`
	Name                 string   `json:"name,omitempty"`
	Disabled             bool     `json:"disabled,omitempty"`
	DailyLimit           int      `json:"daily-limit,omitempty"`
	TotalQuota           int      `json:"total-quota,omitempty"`
	SpendingLimit        float64  `json:"spending-limit,omitempty"`
	ConcurrencyLimit     int      `json:"concurrency-limit,omitempty"`
	RPMLimit             int      `json:"rpm-limit,omitempty"`
	TPMLimit             int      `json:"tpm-limit,omitempty"`
	AllowedModels        []string `json:"allowed-models,omitempty"`
	AllowedChannels      []string `json:"allowed-channels,omitempty"`
	AllowedChannelGroups []string `json:"allowed-channel-groups,omitempty"`
	PermissionProfileID  string   `json:"permission-profile-id,omitempty"`
	SystemPrompt         string   `json:"system-prompt,omitempty"`
	CreatedAt            string   `json:"created-at,omitempty"`
}

func (h *Handler) GetAutoUpdateEnabled(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"enabled": false})
}

func (h *Handler) PutAutoUpdateEnabled(c *gin.Context) {
	unsupportedPanelWrite(c, "auto-update is not available in this v7 build")
}

func (h *Handler) GetAutoUpdateChannel(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"channel": "main"})
}

func (h *Handler) PutAutoUpdateChannel(c *gin.Context) {
	unsupportedPanelWrite(c, "auto-update is not available in this v7 build")
}

func (h *Handler) GetCurrentUpdateState(c *gin.Context) {
	c.JSON(http.StatusOK, h.updateState(false))
}

func (h *Handler) CheckUpdate(c *gin.Context) {
	c.JSON(http.StatusOK, h.updateState(false))
}

func (h *Handler) GetUpdateProgress(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "idle",
		"stage":   "idle",
		"message": "backend self-update is not available in this v7 build",
	})
}

func (h *Handler) ApplyUpdate(c *gin.Context) {
	unsupportedPanelWrite(c, "backend self-update is not available in this v7 build")
}

func (h *Handler) updateState(updaterAvailable bool) gin.H {
	repo := ""
	if h != nil && h.cfg != nil {
		repo = strings.TrimSpace(h.cfg.RemoteManagement.PanelGitHubRepository)
	}
	return gin.H{
		"enabled":            false,
		"current_version":    buildinfo.Version,
		"current_commit":     buildinfo.Commit,
		"current_ui_version": "",
		"current_ui_commit":  "",
		"build_date":         buildinfo.BuildDate,
		"target_channel":     "main",
		"latest_version":     buildinfo.Version,
		"latest_commit":      buildinfo.Commit,
		"latest_ui_version":  "",
		"latest_ui_commit":   "",
		"docker_image":       "ghcr.io/zuohuadong/clirelay",
		"docker_tag":         "main",
		"release_notes":      "",
		"release_url":        repo,
		"update_available":   false,
		"updater_available":  updaterAvailable,
		"message":            "backend self-update is not available in this v7 build",
	}
}

func (h *Handler) GetAPIKeyEntries(c *gin.Context) {
	var entries []panelAPIKeyEntry
	if h != nil {
		entries, _ = h.currentAPIKeyEntries()
	}
	if entries == nil {
		entries = []panelAPIKeyEntry{}
	}
	c.JSON(http.StatusOK, gin.H{"api-key-entries": entries})
}

func (h *Handler) PutAPIKeyEntries(c *gin.Context) {
	entries, ok := readAPIKeyEntries(c)
	if !ok {
		return
	}
	existing, _ := h.currentAPIKeyEntries()
	entries = preserveMaskedAPIKeys(entries, existing)
	if rejectMaskedAPIKeyEntries(c, entries) {
		return
	}
	keys := keysFromEntries(entries)
	h.mu.Lock()
	h.cfg.APIKeys = keys
	h.mu.Unlock()
	if err := h.replaceAPIKeyEntriesInDB(entries); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save api keys"})
		return
	}
	h.persist(c)
	h.refreshAccessProviders()
}

func (h *Handler) PatchAPIKeyEntries(c *gin.Context) {
	var body struct {
		Index *int             `json:"index"`
		Match string           `json:"match"`
		Value panelAPIKeyEntry `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	entries, dbOK := h.currentAPIKeyEntries()
	idx := -1
	if body.Index != nil {
		idx = *body.Index
	} else if body.Match != "" {
		for i, entry := range entries {
			if entry.Key == body.Match {
				idx = i
				break
			}
		}
	}
	if idx < 0 || idx >= len(entries) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
		return
	}
	key := strings.TrimSpace(body.Value.Key)
	if key == "" || isMaskedAPIKey(key) {
		if idx >= 0 && idx < len(entries) {
			key = strings.TrimSpace(entries[idx].Key)
		}
	}
	if key == "" {
		key = strings.TrimSpace(body.Match)
	}
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing key"})
		return
	}
	body.Value.Key = key
	entries[idx] = body.Value
	if entries[idx].CreatedAt == "" {
		entries[idx].CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if rejectMaskedAPIKeyEntries(c, entries) {
		return
	}
	h.updateConfigAPIKeys(keysFromEntries(entries))
	if dbOK {
		if err := h.replaceAPIKeyEntriesInDB(entries); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save api keys"})
			return
		}
	}
	h.persist(c)
	h.refreshAccessProviders()
}

func (h *Handler) DeleteAPIKeyEntries(c *gin.Context) {
	entries, dbOK := h.currentAPIKeyEntries()
	idx := -1
	if raw := strings.TrimSpace(c.Query("index")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			idx = parsed
		}
	}
	if idx < 0 {
		key := strings.TrimSpace(c.Query("key"))
		for i, entry := range entries {
			if entry.Key == key {
				idx = i
				break
			}
		}
	}
	if idx < 0 || idx >= len(entries) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
		return
	}
	entries = append(entries[:idx], entries[idx+1:]...)
	h.updateConfigAPIKeys(keysFromEntries(entries))
	if dbOK {
		if err := h.replaceAPIKeyEntriesInDB(entries); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save api keys"})
			return
		}
	}
	h.persist(c)
	h.refreshAccessProviders()
}

func (h *Handler) GetAPIKeyPermissionProfiles(c *gin.Context) {
	profiles := usage.ListAPIKeyPermissionProfiles()
	if profiles == nil {
		profiles = []usage.APIKeyPermissionProfileRow{}
	}
	c.JSON(http.StatusOK, gin.H{"api-key-permission-profiles": profiles})
}

func (h *Handler) PutAPIKeyPermissionProfiles(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	var profiles []usage.APIKeyPermissionProfileRow
	if err := json.Unmarshal(data, &profiles); err != nil {
		var wrapped struct {
			Profiles []usage.APIKeyPermissionProfileRow `json:"api-key-permission-profiles"`
		}
		if err2 := json.Unmarshal(data, &wrapped); err2 != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		profiles = wrapped.Profiles
	}
	if err := usage.ReplaceAllAPIKeyPermissionProfiles(profiles); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.refreshAccessProviders()
	c.JSON(http.StatusOK, gin.H{"api-key-permission-profiles": profiles})
}

func (h *Handler) GetRoutingConfig(c *gin.Context) {
	routingCfg := currentRoutingConfig(nil)
	if h != nil && h.cfg != nil {
		routingCfg = currentRoutingConfig(h.cfg)
	}
	if h != nil && h.authManager != nil {
		if known, err := collectKnownChannels(h.cfg, h.authManager.List(), ""); err == nil {
			routingCfg = canonicalizeRoutingConfigChannels(routingCfg, known)
		}
	}
	c.JSON(http.StatusOK, routingCfg)
}

func (h *Handler) PutRoutingConfig(c *gin.Context) {
	var body config.RoutingConfig
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	normalized, ok := normalizeRoutingStrategy(body.Strategy)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid routing strategy"})
		return
	}
	body.Strategy = normalized
	nextCfg := &config.Config{}
	if h != nil && h.cfg != nil {
		copied := *h.cfg
		nextCfg = &copied
	}
	nextCfg.Routing = body
	nextCfg.SanitizeRouting()
	var auths []*coreauth.Auth
	if h != nil && h.authManager != nil {
		auths = h.authManager.List()
	}
	if err := validateRoutingAndAPIKeyRestrictions(nextCfg, auths); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.mu.Lock()
	if h.cfg == nil {
		h.cfg = &config.Config{}
	}
	h.cfg.Routing = nextCfg.Routing
	ok = h.persistLocked(c)
	updatedCfg := h.cfg
	h.mu.Unlock()
	if !ok {
		return
	}
	if h.authManager != nil {
		h.authManager.SetConfig(updatedCfg)
	}
	managementasset.SetCurrentConfig(updatedCfg)
}

func (h *Handler) GetProxyPool(c *gin.Context) {
	items := make([]gin.H, 0)
	if h != nil && h.cfg != nil {
		for _, entry := range h.cfg.ProxyPool {
			items = append(items, gin.H{
				"id":          entry.ID,
				"name":        entry.Name,
				"url":         entry.URL,
				"enabled":     entry.Enabled,
				"description": entry.Description,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) PutProxyPool(c *gin.Context) {
	var body struct {
		Items []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			URL         string `json:"url"`
			Enabled     bool   `json:"enabled"`
			Description string `json:"description"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	entries := make([]config.ProxyPoolEntry, 0, len(body.Items))
	for _, item := range body.Items {
		entries = append(entries, config.ProxyPoolEntry{
			ID:          strings.TrimSpace(item.ID),
			Name:        strings.TrimSpace(item.Name),
			URL:         strings.TrimSpace(item.URL),
			Enabled:     item.Enabled,
			Description: strings.TrimSpace(item.Description),
		})
	}
	h.mu.Lock()
	h.cfg.ProxyPool = config.NormalizeProxyPool(entries)
	h.mu.Unlock()
	h.persist(c)
}

func (h *Handler) CheckProxyPool(c *gin.Context) {
	var body struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	proxyURL := strings.TrimSpace(body.URL)
	if proxyURL == "" && strings.TrimSpace(body.ID) != "" {
		id := strings.TrimSpace(body.ID)
		if h != nil && h.cfg != nil {
			for _, entry := range h.cfg.ProxyPool {
				if entry.ID == id || entry.Name == id {
					proxyURL = strings.TrimSpace(entry.URL)
					break
				}
			}
		}
	}
	if proxyURL == "" {
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": "no proxy url resolved"})
		return
	}
	transport := &http.Transport{
		Proxy: http.ProxyURL(mustParseURL(proxyURL)),
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Get("https://httpbin.org/ip")
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"ok":         false,
			"latency_ms": latencyMs,
			"message":    err.Error(),
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 400
	c.JSON(http.StatusOK, gin.H{
		"ok":          ok,
		"latency_ms":  latencyMs,
		"status_code": resp.StatusCode,
		"message":     "",
	})
}

func mustParseURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	return u
}

func (h *Handler) GetCCSwitchImportConfigs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ccswitch-import-configs": []gin.H{}})
}

func (h *Handler) PutCCSwitchImportConfigs(c *gin.Context) {
	unsupportedPanelWrite(c, "ccswitch import presets are not available in this v7 build")
}

func (h *Handler) GetOpenCodeGoKeys(c *gin.Context) {
	h.mu.Lock()
	items := append([]config.OpenCodeGoKey(nil), h.cfg.OpenCodeGoKey...)
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"opencode-go-api-key": items})
}

func (h *Handler) PutOpenCodeGoKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.OpenCodeGoKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.OpenCodeGoKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.OpenCodeGoKey = append([]config.OpenCodeGoKey(nil), arr...)
	h.cfg.SanitizeOpenCodeGoKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteOpenCodeGoKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			out := make([]config.OpenCodeGoKey, 0, len(h.cfg.OpenCodeGoKey))
			for _, v := range h.cfg.OpenCodeGoKey {
				if strings.TrimSpace(v.APIKey) == val && strings.TrimSpace(v.BaseURL) == base {
					continue
				}
				out = append(out, v)
			}
			if len(out) != len(h.cfg.OpenCodeGoKey) {
				h.cfg.OpenCodeGoKey = out
				h.cfg.SanitizeOpenCodeGoKeys()
				h.persistLocked(c)
			} else {
				c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
			}
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.OpenCodeGoKey {
			if strings.TrimSpace(h.cfg.OpenCodeGoKey[i].APIKey) == val {
				matchCount++
				if matchIndex == -1 {
					matchIndex = i
				}
			}
		}
		if matchCount == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
			return
		}
		if matchCount > 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "multiple items match api-key; base-url is required"})
			return
		}
		h.cfg.OpenCodeGoKey = append(h.cfg.OpenCodeGoKey[:matchIndex], h.cfg.OpenCodeGoKey[matchIndex+1:]...)
		h.cfg.SanitizeOpenCodeGoKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		idx, errParse := strconv.Atoi(idxStr)
		if errParse == nil && idx >= 0 && idx < len(h.cfg.OpenCodeGoKey) {
			h.cfg.OpenCodeGoKey = append(h.cfg.OpenCodeGoKey[:idx], h.cfg.OpenCodeGoKey[idx+1:]...)
			h.cfg.SanitizeOpenCodeGoKeys()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "missing api-key or index"})
}

func (h *Handler) GetBedrockKeys(c *gin.Context) {
	h.mu.Lock()
	items := append([]config.BedrockKey(nil), h.cfg.BedrockKey...)
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"bedrock-api-key": items})
}

func (h *Handler) PutBedrockKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.BedrockKey
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.BedrockKey `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.BedrockKey = append([]config.BedrockKey(nil), arr...)
	h.cfg.SanitizeBedrockKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteBedrockKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.BedrockKey = nil
	h.persistLocked(c)
}

func (h *Handler) GetIFlowKeys(c *gin.Context) {
	items := h.iflowWithAuthIndex()
	c.JSON(http.StatusOK, gin.H{
		"iflow":         items,
		"iflow-api-key": items,
	})
}

func (h *Handler) PutIFlowKeys(c *gin.Context) {
	unsupportedPanelWrite(c, "iflow provider uses OAuth authentication; use the iflow-auth-url endpoint instead")
}

func (h *Handler) PatchIFlowKey(c *gin.Context) {
	unsupportedPanelWrite(c, "iflow provider uses OAuth authentication; use the iflow-auth-url endpoint instead")
}

func (h *Handler) DeleteIFlowKey(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) RequestUnsupportedOAuthProvider(c *gin.Context) {
	unsupportedPanelWrite(c, "this OAuth provider is not available in this v7 build")
}

func (h *Handler) GetImageGenerationChannels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"items": []gin.H{}})
}

func (h *Handler) StartImageGenerationTest(c *gin.Context) {
	unsupportedPanelWrite(c, "image generation test tasks are not available in this v7 build")
}

func (h *Handler) GetImageGenerationTest(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"task_id": c.Param("task_id"),
		"status":  "unsupported",
		"message": "image generation test tasks are not available in this v7 build",
	})
}

func (h *Handler) GetModels(c *gin.Context) {
	models := registry.GetGlobalRegistry().GetAvailableModels("openai")
	if len(models) == 0 {
		models = modelMapsFromInfos(allStaticModelInfos(), true, "")
	}
	c.JSON(http.StatusOK, gin.H{"data": models})
}

func (h *Handler) GetModelConfigs(c *gin.Context) {
	scope := strings.ToLower(strings.TrimSpace(c.DefaultQuery("scope", "active")))
	models := modelMapsFromInfos(allStaticModelInfos(), true, scope)
	if scope == "active" {
		if available := registry.GetGlobalRegistry().GetAvailableModels("openai"); len(available) > 0 {
			models = available
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": models})
}

func (h *Handler) GetModelOwnerPresets(c *gin.Context) {
	counts := map[string]int{}
	labels := map[string]string{}
	for _, model := range allStaticModelInfos() {
		owner := normalizeOwner(model.OwnedBy)
		if owner == "" {
			owner = normalizeOwner(model.Type)
		}
		if owner == "" {
			continue
		}
		counts[owner]++
		if labels[owner] == "" {
			labels[owner] = model.OwnedBy
			if strings.TrimSpace(labels[owner]) == "" {
				labels[owner] = owner
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]gin.H, 0, len(keys))
	for _, key := range keys {
		items = append(items, gin.H{
			"value":       key,
			"label":       labels[key],
			"description": "",
			"enabled":     true,
			"modelCount":  counts[key],
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) GetConfiguredModelAvailability(c *gin.Context) {
	availableModels := registry.GetGlobalRegistry().GetAvailableModels("openai")

	availableIDs := make(map[string]bool, len(availableModels))
	for _, m := range availableModels {
		if id, ok := m["id"].(string); ok {
			availableIDs[id] = true
		}
	}

	staticModels := allStaticModelInfos()
	items := make([]gin.H, 0, len(staticModels))
	for _, model := range staticModels {
		_, configured := availableIDs[model.ID]
		items = append(items, gin.H{
			"id":         model.ID,
			"owned_by":   model.OwnedBy,
			"kind":       "canonical",
			"alias":      false,
			"configured": configured,
			"available":  configured,
			"paths": []gin.H{
				{"scope": "openai", "label": "OpenAI Chat", "method": "POST", "path": "/v1/chat/completions", "family": "openai"},
				{"scope": "openai", "label": "OpenAI Responses", "method": "POST", "path": "/v1/responses", "family": "openai"},
			},
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"items":            items,
		"total":            len(items),
		"configured_count": len(availableIDs),
	})
}

func (h *Handler) GetModelPathAvailability(c *gin.Context) {
	models := allStaticModelInfos()
	items := make([]gin.H, 0, len(models))
	for _, model := range models {
		items = append(items, gin.H{
			"id":       model.ID,
			"owned_by": model.OwnedBy,
			"kind":     "canonical",
			"alias":    false,
			"paths": []gin.H{
				{"scope": "openai", "label": "OpenAI Chat", "method": "POST", "path": "/v1/chat/completions", "family": "openai"},
				{"scope": "openai", "label": "OpenAI Responses", "method": "POST", "path": "/v1/responses", "family": "openai"},
			},
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"routes": []gin.H{
			{
				"label":    "OpenAI compatible",
				"path":     "/v1",
				"system":   true,
				"readOnly": true,
				"capabilities": []gin.H{
					{"label": "Chat Completions", "method": "POST", "path": "/v1/chat/completions", "family": "openai"},
					{"label": "Responses", "method": "POST", "path": "/v1/responses", "family": "openai"},
				},
			},
		},
	})
}

func (h *Handler) usageDB() (*sql.DB, bool) {
	return h.openAPIKeysDB()
}

func (h *Handler) GetUsageSummary(c *gin.Context) {
	db, ok := h.usageDB()
	if !ok {
		c.JSON(http.StatusOK, gin.H{"usage": gin.H{"total_requests": 0, "success_count": 0, "failure_count": 0, "total_tokens": 0, "apis": gin.H{}, "requests_by_day": gin.H{}, "requests_by_hour": gin.H{}, "tokens_by_day": gin.H{}, "tokens_by_hour": gin.H{}}})
		return
	}
	defer func() { _ = db.Close() }()
	filters := usageFiltersFromQuery(c)
	summary := queryUsageTotals(db, filters)
	reqsByDay := gin.H{}
	toksByDay := gin.H{}
	for _, point := range queryUsageDailySeries(db, filters) {
		reqsByDay[point["date"].(string)] = point["requests"]
		toksByDay[point["date"].(string)] = point["total_tokens"]
	}

	c.JSON(http.StatusOK, gin.H{"usage": gin.H{"total_requests": summary.Total, "success_count": summary.Success, "failure_count": summary.Failed, "total_tokens": summary.TotalTokens, "apis": gin.H{}, "requests_by_day": reqsByDay, "requests_by_hour": gin.H{}, "tokens_by_day": toksByDay, "tokens_by_hour": gin.H{}}})
}

func (h *Handler) GetUsageSummaryPublic(c *gin.Context) {
	db, ok := h.usageDB()
	if !ok {
		c.JSON(http.StatusOK, gin.H{
			"total_requests": 0,
			"success_count":  0,
			"failure_count":  0,
			"total_tokens":   0,
		})
		return
	}
	defer func() { _ = db.Close() }()

	if !dbTableExists(db, "request_logs") {
		c.JSON(http.StatusOK, gin.H{
			"total_requests": 0,
			"success_count":  0,
			"failure_count":  0,
			"total_tokens":   0,
		})
		return
	}

	cols := requestLogColumns(db)
	row := db.QueryRow("SELECT count(*), coalesce(sum(case when " + usageColumnExpr(cols, "failed", "0") + "=0 then 1 else 0 end),0), coalesce(sum(case when " + usageColumnExpr(cols, "failed", "0") + "!=0 then 1 else 0 end),0), coalesce(sum(" + usageColumnExpr(cols, "total_tokens", "0") + "),0) FROM request_logs")

	var totalRequests, successCount, failureCount, totalTokens int64
	_ = row.Scan(&totalRequests, &successCount, &failureCount, &totalTokens)

	c.JSON(http.StatusOK, gin.H{
		"total_requests": totalRequests,
		"success_count":  successCount,
		"failure_count":  failureCount,
		"total_tokens":   totalTokens,
	})
}

func (h *Handler) GetUsageChartData(c *gin.Context) {
	db, ok := h.usageDB()
	if !ok {
		c.JSON(http.StatusOK, emptyUsageChartPayload())
		return
	}
	defer func() { _ = db.Close() }()
	c.JSON(http.StatusOK, buildUsageChartPayload(db, usageFiltersFromQuery(c), false))
}

func (h *Handler) GetUsageEntityStats(c *gin.Context) {
	db, ok := h.usageDB()
	if !ok {
		c.JSON(http.StatusOK, gin.H{"source": []gin.H{}, "auth_index": []gin.H{}})
		return
	}
	defer func() { _ = db.Close() }()

	filters := usageFiltersFromQuery(c)
	sourceSeen := make(map[string]bool)
	sourceStats := make([]gin.H, 0)
	cols := requestLogColumns(db)
	sourceFilters := filters
	if len(sourceFilters.Sources) > 0 {
		sourceFilters.AuthIndexes = nil
	}
	sourceWhereSQL, sourceArgs := sourceFilters.whereClause(db)
	rows, _ := db.Query("SELECT "+usageColumnExpr(cols, "source", "''")+", count(*), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"=0 then 1 else 0 end),0), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"!=0 then 1 else 0 end),0), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0), coalesce(avg("+usageColumnExpr(cols, "latency_ms", "0")+"),0) FROM request_logs WHERE "+sourceWhereSQL+" GROUP BY "+usageColumnExpr(cols, "source", "''")+" ORDER BY count(*) DESC LIMIT 100", sourceArgs...)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var source string
			var total, successCnt, failCnt, toks int64
			var avgLatency float64
			if rows.Scan(&source, &total, &successCnt, &failCnt, &toks, &avgLatency) == nil {
				sourceStats = append(sourceStats, gin.H{"entity_name": source, "source": source, "requests": total, "failed": failCnt, "tokens": toks, "total_tokens": toks, "avg_latency": avgLatency})
				sourceSeen[source] = true
			}
		}
	}

	authIndexToAPIKey := h.buildAuthIndexToAPIKeyMap()

	authStats := make([]gin.H, 0)
	authFilters := filters
	if len(authFilters.AuthIndexes) > 0 {
		authFilters.Sources = nil
	}
	authWhereSQL, authArgs := authFilters.whereClause(db)
	rows2, _ := db.Query("SELECT "+usageColumnExpr(cols, "auth_index", "''")+", count(*), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"=0 then 1 else 0 end),0), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"!=0 then 1 else 0 end),0), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0), coalesce(avg("+usageColumnExpr(cols, "latency_ms", "0")+"),0) FROM request_logs WHERE "+authWhereSQL+" GROUP BY "+usageColumnExpr(cols, "auth_index", "''")+" ORDER BY count(*) DESC LIMIT 100", authArgs...)
	if rows2 != nil {
		defer rows2.Close()
		for rows2.Next() {
			var idx string
			var total, successCnt, failCnt, toks int64
			var avgLatency float64
			if rows2.Scan(&idx, &total, &successCnt, &failCnt, &toks, &avgLatency) == nil {
				authStats = append(authStats, gin.H{"entity_name": idx, "auth_index": idx, "requests": total, "failed": failCnt, "tokens": toks, "total_tokens": toks, "avg_latency": avgLatency})
				if apiKey, found := authIndexToAPIKey[idx]; found && apiKey != "" && !sourceSeen[apiKey] {
					sourceStats = append(sourceStats, gin.H{"entity_name": apiKey, "source": apiKey, "requests": total, "failed": failCnt, "tokens": toks, "total_tokens": toks, "avg_latency": avgLatency})
					sourceSeen[apiKey] = true
				}
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"source": sourceStats, "auth_index": authStats})
}

func (h *Handler) buildAuthIndexToAPIKeyMap() map[string]string {
	out := make(map[string]string)
	if h == nil {
		return out
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return out
	}
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		idx := strings.TrimSpace(auth.Index)
		if idx == "" {
			idx = auth.EnsureIndex()
		}
		if idx == "" {
			continue
		}
		if auth.Attributes != nil {
			if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
				out[idx] = key
			}
		}
	}
	return out
}

func (h *Handler) GetUsageLogs(c *gin.Context) {
	db, ok := h.usageDB()
	if !ok {
		c.JSON(http.StatusOK, emptyUsageLogsPayload(positiveQueryInt(c, "page", 1), positiveQueryInt(c, "size", 50), false))
		return
	}
	defer func() { _ = db.Close() }()
	c.JSON(http.StatusOK, buildUsageLogsPayload(db, usageFiltersFromQuery(c), false))
}

func (h *Handler) GetPublicUsageLogs(c *gin.Context) {
	setPublicUsageNoStore(c)
	req, ok := readPublicUsageRequest(c)
	if !ok {
		return
	}
	db, dbOK := h.usageDB()
	if !dbOK {
		c.JSON(http.StatusOK, emptyUsageLogsPayload(req.Page, req.Size, true))
		return
	}
	defer func() { _ = db.Close() }()
	c.JSON(http.StatusOK, buildUsageLogsPayload(db, req.filters(), true))
}

func (h *Handler) GetPublicUsageChartData(c *gin.Context) {
	setPublicUsageNoStore(c)
	req, ok := readPublicUsageRequest(c)
	if !ok {
		return
	}
	db, dbOK := h.usageDB()
	if !dbOK {
		c.JSON(http.StatusOK, emptyUsageChartPayload())
		return
	}
	defer func() { _ = db.Close() }()
	c.JSON(http.StatusOK, buildUsageChartPayload(db, req.filters(), true))
}

func (h *Handler) GetPublicUsageSummary(c *gin.Context) {
	setPublicUsageNoStore(c)
	req, ok := readPublicUsageRequest(c)
	if !ok {
		return
	}
	db, dbOK := h.usageDB()
	if !dbOK {
		c.JSON(http.StatusOK, publicUsageSummaryPayload(req.APIKey, usageTotals{}, false))
		return
	}
	defer func() { _ = db.Close() }()
	filters := req.filters()
	totals := queryUsageTotals(db, filters)
	c.JSON(http.StatusOK, publicUsageSummaryPayload(req.APIKey, totals, usageAPIKeyExists(db, req.APIKey)))
}

func (h *Handler) GetUsageLogContent(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db, ok := h.usageDB()
	if !ok {
		c.JSON(http.StatusOK, gin.H{"id": id, "model": "", "input_content": "", "output_content": "", "detail_content": ""})
		return
	}
	defer func() { _ = db.Close() }()
	writeUsageLogContent(c, db, id, "", strings.TrimSpace(c.Query("part")))
}

func (h *Handler) GetPublicUsageLogContent(c *gin.Context) {
	setPublicUsageNoStore(c)
	req, ok := readPublicUsageRequest(c)
	if !ok {
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db, dbOK := h.usageDB()
	if !dbOK {
		c.JSON(http.StatusOK, gin.H{"id": id, "model": "", "part": req.Part, "content": ""})
		return
	}
	defer func() { _ = db.Close() }()
	writeUsageLogContent(c, db, id, req.APIKey, req.Part)
}

func (h *Handler) DeleteUsageLogs(c *gin.Context) {
	var req struct {
		ClearBodyContent    bool `json:"clear_body_content"`
		ClearDetailContent  bool `json:"clear_detail_content"`
		ClearRequestRecords bool `json:"clear_request_records"`
	}
	req.ClearBodyContent = true
	req.ClearDetailContent = true
	if c.Request != nil && c.Request.Body != nil {
		_ = c.ShouldBindJSON(&req)
	}
	if !req.ClearBodyContent && !req.ClearDetailContent && !req.ClearRequestRecords {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no cleanup target selected"})
		return
	}
	db, ok := h.usageDB()
	if !ok {
		c.JSON(http.StatusOK, gin.H{"deleted_logs": 0, "deleted_contents": 0, "cleared_body_rows": 0, "cleared_detail_rows": 0, "cleared_legacy_rows": 0})
		return
	}
	defer func() { _ = db.Close() }()
	c.JSON(http.StatusOK, clearUsageLogData(db, req.ClearBodyContent, req.ClearDetailContent, req.ClearRequestRecords))
}

func (h *Handler) ExportUsage(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"version": 1, "exported_at": time.Now().UTC().Format(time.RFC3339), "usage": gin.H{}})
}

func (h *Handler) ImportUsage(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"added": 0, "skipped": 0, "total_requests": 0, "failed_requests": 0})
}

func (h *Handler) RecordAuthFileQuotaSnapshot(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) GetAuthFileGroupTrend(c *gin.Context) {
	days := positiveQueryInt(c, "days", 7)
	c.JSON(http.StatusOK, gin.H{
		"days":         days,
		"group":        c.Query("group"),
		"points":       []gin.H{},
		"quota_points": []gin.H{},
	})
}

func (h *Handler) GetAuthFileTrend(c *gin.Context) {
	days := positiveQueryInt(c, "days", 7)
	hours := positiveQueryInt(c, "hours", 5)
	c.JSON(http.StatusOK, gin.H{
		"auth_index":          c.Query("auth_index"),
		"days":                days,
		"hours":               hours,
		"request_total":       0,
		"cycle_request_total": 0,
		"cycle_start":         "",
		"daily_usage":         []gin.H{},
		"hourly_usage":        []gin.H{},
		"quota_series":        []gin.H{},
	})
}

func (h *Handler) ReconcileQuota(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

type usageFilters struct {
	Page        int
	Size        int
	Days        int
	APIKey      string
	Model       string
	Channel     string
	Status      string
	Failed      string
	Start       string
	End         string
	AuthIndexes []string
	Sources     []string
}

type usageTotals struct {
	Total           int64
	Success         int64
	Failed          int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
	TotalCost       float64
}

type publicUsageRequest struct {
	APIKey string `json:"api_key"`
	Page   int    `json:"page"`
	Size   int    `json:"size"`
	Days   int    `json:"days"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Part   string `json:"part"`
	Format string `json:"format"`
}

func usageFiltersFromQuery(c *gin.Context) usageFilters {
	return usageFilters{
		Page:        positiveQueryInt(c, "page", 1),
		Size:        positiveQueryInt(c, "size", 50),
		Days:        positiveQueryInt(c, "days", 7),
		APIKey:      strings.TrimSpace(c.Query("api_key")),
		Model:       strings.TrimSpace(c.Query("model")),
		Channel:     strings.TrimSpace(c.Query("channel")),
		Status:      strings.TrimSpace(c.Query("status")),
		Failed:      strings.TrimSpace(c.Query("failed")),
		Start:       strings.TrimSpace(c.Query("start")),
		End:         strings.TrimSpace(c.Query("end")),
		AuthIndexes: cleanQueryArray(c.QueryArray("auth_index")),
		Sources:     cleanQueryArray(c.QueryArray("source")),
	}
}

func (r publicUsageRequest) filters() usageFilters {
	page := r.Page
	if page <= 0 {
		page = 1
	}
	size := r.Size
	if size <= 0 {
		size = 50
	}
	days := r.Days
	if days <= 0 {
		days = 7
	}
	return usageFilters{
		Page:   page,
		Size:   size,
		Days:   days,
		APIKey: strings.TrimSpace(r.APIKey),
		Model:  strings.TrimSpace(r.Model),
		Status: strings.TrimSpace(r.Status),
	}
}

func readPublicUsageRequest(c *gin.Context) (publicUsageRequest, bool) {
	req := publicUsageRequest{
		APIKey: strings.TrimSpace(c.Query("api_key")),
		Page:   positiveQueryInt(c, "page", 1),
		Size:   positiveQueryInt(c, "size", 50),
		Days:   positiveQueryInt(c, "days", 7),
		Model:  strings.TrimSpace(c.Query("model")),
		Status: strings.TrimSpace(c.Query("status")),
		Part:   strings.TrimSpace(c.Query("part")),
		Format: strings.TrimSpace(c.Query("format")),
	}
	if c.Request != nil && c.Request.Body != nil {
		var body publicUsageRequest
		if err := c.ShouldBindJSON(&body); err == nil {
			if strings.TrimSpace(body.APIKey) != "" {
				req.APIKey = strings.TrimSpace(body.APIKey)
			}
			if body.Page > 0 {
				req.Page = body.Page
			}
			if body.Size > 0 {
				req.Size = body.Size
			}
			if body.Days > 0 {
				req.Days = body.Days
			}
			if strings.TrimSpace(body.Model) != "" {
				req.Model = strings.TrimSpace(body.Model)
			}
			if strings.TrimSpace(body.Status) != "" {
				req.Status = strings.TrimSpace(body.Status)
			}
			if strings.TrimSpace(body.Part) != "" {
				req.Part = strings.TrimSpace(body.Part)
			}
			if strings.TrimSpace(body.Format) != "" {
				req.Format = strings.TrimSpace(body.Format)
			}
		}
	}
	if strings.TrimSpace(req.APIKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing api_key"})
		return req, false
	}
	return req, true
}

func setPublicUsageNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
}

func cleanQueryArray(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (f usageFilters) whereClause(db *sql.DB) (string, []any) {
	cols := requestLogColumns(db)
	parts := []string{"1=1"}
	args := []any{}
	if f.Days > 0 && cols["timestamp"] {
		parts = append(parts, "datetime(timestamp) >= datetime('now', ?)")
		args = append(args, "-"+strconv.Itoa(f.Days)+" days")
	}
	if f.Start != "" && cols["timestamp"] {
		parts = append(parts, "datetime(timestamp) >= datetime(?)")
		args = append(args, f.Start)
	}
	if f.End != "" && cols["timestamp"] {
		parts = append(parts, "datetime(timestamp) <= datetime(?)")
		args = append(args, f.End)
	}
	if f.Model != "" && cols["model"] {
		parts = append(parts, "model = ?")
		args = append(args, f.Model)
	}
	if f.Channel != "" && cols["channel_name"] {
		parts = append(parts, "channel_name = ?")
		args = append(args, f.Channel)
	}
	if f.APIKey != "" && cols["api_key"] {
		if f.APIKey == "__system__" {
			parts = append(parts, "(coalesce(api_key, '') = '' OR api_key LIKE '/%' OR api_key LIKE 'GET /%' OR api_key LIKE 'POST /%' OR api_key LIKE 'PUT /%' OR api_key LIKE 'PATCH /%' OR api_key LIKE 'DELETE /%' OR api_key LIKE 'OPTIONS /%' OR api_key LIKE 'HEAD /%')")
		} else {
			parts = append(parts, "api_key = ?")
			args = append(args, f.APIKey)
		}
	}
	status := strings.ToLower(strings.TrimSpace(f.Status))
	failed := strings.TrimSpace(f.Failed)
	if cols["failed"] {
		switch {
		case status == "failed" || failed == "1" || strings.EqualFold(failed, "true"):
			parts = append(parts, "failed != 0")
		case status == "success" || failed == "0" || strings.EqualFold(failed, "false"):
			parts = append(parts, "failed = 0")
		}
	}
	if len(f.AuthIndexes) > 0 && cols["auth_index"] {
		parts = append(parts, "auth_index IN ("+usagePlaceholders(len(f.AuthIndexes))+")")
		for _, value := range f.AuthIndexes {
			args = append(args, value)
		}
	}
	if len(f.Sources) > 0 && cols["source"] {
		parts = append(parts, "source IN ("+usagePlaceholders(len(f.Sources))+")")
		for _, value := range f.Sources {
			args = append(args, value)
		}
	}
	return strings.Join(parts, " AND "), args
}

func usagePlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func emptyUsageChartPayload() gin.H {
	return gin.H{"daily_series": []gin.H{}, "model_distribution": []gin.H{}, "hourly_tokens": []gin.H{}, "hourly_models": []gin.H{}, "apikey_distribution": []gin.H{}, "stats": gin.H{"total": 0, "success_rate": 0, "total_tokens": 0, "total_cost": 0}}
}

func emptyUsageLogsPayload(page, size int, public bool) gin.H {
	filters := gin.H{"models": []string{}}
	if !public {
		filters["api_keys"] = []string{}
		filters["api_key_names"] = gin.H{}
		filters["channels"] = []string{}
	}
	return gin.H{"items": []gin.H{}, "total": 0, "page": page, "size": size, "filters": filters, "stats": gin.H{"total": 0, "success_rate": 0, "total_tokens": 0, "total_cost": 0}}
}

func buildUsageChartPayload(db *sql.DB, filters usageFilters, public bool) gin.H {
	payload := emptyUsageChartPayload()
	payload["daily_series"] = queryUsageDailySeries(db, filters)
	payload["model_distribution"] = queryUsageModelDistribution(db, filters)
	payload["hourly_tokens"] = queryUsageHourlyTokens(db, filters)
	payload["hourly_models"] = queryUsageHourlyModels(db, filters)
	if !public {
		payload["apikey_distribution"] = queryUsageAPIKeyDistribution(db, filters)
	}
	totals := queryUsageTotals(db, filters)
	successRate := float64(0)
	if totals.Total > 0 {
		successRate = float64(totals.Success) / float64(totals.Total) * 100
	}
	payload["stats"] = gin.H{"total": totals.Total, "success_rate": successRate, "total_tokens": totals.TotalTokens, "total_cost": totals.TotalCost}
	return payload
}

func queryUsageTotals(db *sql.DB, filters usageFilters) usageTotals {
	if !dbTableExists(db, "request_logs") {
		return usageTotals{}
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	row := db.QueryRow("SELECT count(*), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"=0 then 1 else 0 end),0), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"!=0 then 1 else 0 end),0), coalesce(sum("+usageColumnExpr(cols, "input_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "output_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "reasoning_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "cached_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "cost", "0")+"),0) FROM request_logs WHERE "+whereSQL, args...)
	var out usageTotals
	_ = row.Scan(&out.Total, &out.Success, &out.Failed, &out.InputTokens, &out.OutputTokens, &out.ReasoningTokens, &out.CachedTokens, &out.TotalTokens, &out.TotalCost)
	return out
}

func queryUsageDailySeries(db *sql.DB, filters usageFilters) []gin.H {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.Query("SELECT date("+usageColumnExpr(cols, "timestamp", "datetime('now')")+") as d, count(*) as cnt, coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"!=0 then 1 else 0 end),0), coalesce(sum("+usageColumnExpr(cols, "input_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "output_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "reasoning_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "cached_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "cost", "0")+"),0) FROM request_logs WHERE "+whereSQL+" GROUP BY d ORDER BY d", args...)
	if err != nil || rows == nil {
		return []gin.H{}
	}
	defer func() { _ = rows.Close() }()
	out := []gin.H{}
	for rows.Next() {
		var date string
		var requests, failed, input, output, reasoning, cached, total int64
		var cost float64
		if rows.Scan(&date, &requests, &failed, &input, &output, &reasoning, &cached, &total, &cost) == nil {
			out = append(out, gin.H{"date": date, "requests": requests, "failed_requests": failed, "input_tokens": input, "output_tokens": output, "reasoning_tokens": reasoning, "cached_tokens": cached, "total_tokens": total, "tokens": total, "cost": cost, "total_cost": cost})
		}
	}
	return out
}

func queryUsageModelDistribution(db *sql.DB, filters usageFilters) []gin.H {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.Query("SELECT "+usageColumnExpr(cols, "model", "''")+", count(*) as cnt, coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0) FROM request_logs WHERE "+whereSQL+" GROUP BY "+usageColumnExpr(cols, "model", "''")+" ORDER BY cnt DESC LIMIT 20", args...)
	if err != nil || rows == nil {
		return []gin.H{}
	}
	defer func() { _ = rows.Close() }()
	out := []gin.H{}
	for rows.Next() {
		var model string
		var requests, tokens int64
		if rows.Scan(&model, &requests, &tokens) == nil {
			out = append(out, gin.H{"model": model, "requests": requests, "tokens": tokens, "count": requests})
		}
	}
	return out
}

func queryUsageHourlyTokens(db *sql.DB, filters usageFilters) []gin.H {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.Query("SELECT strftime('%Y-%m-%d %H:00', "+usageColumnExpr(cols, "timestamp", "datetime('now')")+"), coalesce(sum("+usageColumnExpr(cols, "input_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "output_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "reasoning_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "cached_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0) FROM request_logs WHERE "+whereSQL+" GROUP BY 1 ORDER BY 1", args...)
	if err != nil || rows == nil {
		return []gin.H{}
	}
	defer func() { _ = rows.Close() }()
	out := []gin.H{}
	for rows.Next() {
		var hour string
		var input, output, reasoning, cached, total int64
		if rows.Scan(&hour, &input, &output, &reasoning, &cached, &total) == nil {
			out = append(out, gin.H{"hour": hour, "input_tokens": input, "output_tokens": output, "reasoning_tokens": reasoning, "cached_tokens": cached, "total_tokens": total})
		}
	}
	return out
}

func queryUsageHourlyModels(db *sql.DB, filters usageFilters) []gin.H {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.Query("SELECT strftime('%Y-%m-%d %H:00', "+usageColumnExpr(cols, "timestamp", "datetime('now')")+"), "+usageColumnExpr(cols, "model", "''")+", count(*) FROM request_logs WHERE "+whereSQL+" GROUP BY 1, 2 ORDER BY 1, 3 DESC", args...)
	if err != nil || rows == nil {
		return []gin.H{}
	}
	defer func() { _ = rows.Close() }()
	out := []gin.H{}
	for rows.Next() {
		var hour, model string
		var requests int64
		if rows.Scan(&hour, &model, &requests) == nil {
			out = append(out, gin.H{"hour": hour, "model": model, "requests": requests})
		}
	}
	return out
}

func queryUsageHourlyThroughput(db *sql.DB, filters usageFilters) []gin.H {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.Query("SELECT strftime('%Y-%m-%d %H:00', "+usageColumnExpr(cols, "timestamp", "datetime('now')")+"), count(*), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0) FROM request_logs WHERE "+whereSQL+" GROUP BY 1 ORDER BY 1", args...)
	if err != nil || rows == nil {
		return []gin.H{}
	}
	defer func() { _ = rows.Close() }()
	out := []gin.H{}
	for rows.Next() {
		var hour string
		var requests, tokens int64
		if rows.Scan(&hour, &requests, &tokens) == nil {
			out = append(out, gin.H{"hour": hour, "label": hour, "requests": requests, "rpm": float64(requests) / 60, "total_tokens": tokens, "tpm": float64(tokens) / 60})
		}
	}
	return out
}

func queryUsageAPIKeyDistribution(db *sql.DB, filters usageFilters) []gin.H {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	joinSQL, keyExpr, nameExpr := usageAPIKeyNameLookup(db, cols)
	rows, err := db.Query("SELECT "+keyExpr+", "+nameExpr+", count(*) as cnt, coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0) FROM request_logs"+joinSQL+" WHERE "+whereSQL+" GROUP BY "+keyExpr+", "+nameExpr+" ORDER BY cnt DESC LIMIT 20", args...)
	if err != nil || rows == nil {
		return []gin.H{}
	}
	defer func() { _ = rows.Close() }()
	out := []gin.H{}
	for rows.Next() {
		var key, name string
		var requests, tokens int64
		if rows.Scan(&key, &name, &requests, &tokens) == nil {
			out = append(out, gin.H{"api_key": key, "name": name, "requests": requests, "tokens": tokens, "count": requests})
		}
	}
	return out
}

func buildUsageLogsPayload(db *sql.DB, filters usageFilters, public bool) gin.H {
	if !dbTableExists(db, "request_logs") {
		return emptyUsageLogsPayload(filters.Page, filters.Size, public)
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	totalStats := queryUsageTotals(db, filters)
	successRate := float64(0)
	if totalStats.Total > 0 {
		successRate = float64(totalStats.Success) / float64(totalStats.Total) * 100
	}

	offset := (filters.Page - 1) * filters.Size
	joinSQL, keyExpr, nameExpr := usageAPIKeyNameLookup(db, cols)
	selectCols := strings.Join([]string{
		usageColumnExpr(cols, "id", "0"),
		usageColumnExpr(cols, "timestamp", "''"),
		keyExpr,
		nameExpr,
		usageColumnExpr(cols, "model", "''"),
		usageColumnExpr(cols, "source", "''"),
		usageColumnExpr(cols, "channel_name", "''"),
		usageColumnExpr(cols, "auth_index", "''"),
		usageColumnExpr(cols, "failed", "0"),
		usageColumnExpr(cols, "latency_ms", "0"),
		usageColumnExpr(cols, "first_token_ms", "0"),
		usageColumnExpr(cols, "input_tokens", "0"),
		usageColumnExpr(cols, "output_tokens", "0"),
		usageColumnExpr(cols, "reasoning_tokens", "0"),
		usageColumnExpr(cols, "cached_tokens", "0"),
		usageColumnExpr(cols, "total_tokens", "0"),
		usageColumnExpr(cols, "cost", "0"),
	}, ", ")
	rows, err := db.Query("SELECT "+selectCols+" FROM request_logs"+joinSQL+" WHERE "+whereSQL+" ORDER BY "+usageColumnExpr(cols, "id", "rowid")+" DESC LIMIT ? OFFSET ?", append(args, filters.Size, offset)...)
	items := make([]gin.H, 0)
	if err == nil && rows != nil {
		defer rows.Close()
		for rows.Next() {
			var id, failed, latency, firstTok, inTok, outTok, reasonTok, cachedTok, totalTok int64
			var ts, apiKey, apiKeyName, model, source, channel, authIdx string
			var cost float64
			if rows.Scan(&id, &ts, &apiKey, &apiKeyName, &model, &source, &channel, &authIdx, &failed, &latency, &firstTok, &inTok, &outTok, &reasonTok, &cachedTok, &totalTok, &cost) == nil {
				item := gin.H{"id": id, "timestamp": ts, "model": model, "failed": failed != 0, "latency_ms": latency, "first_token_ms": firstTok, "input_tokens": inTok, "output_tokens": outTok, "reasoning_tokens": reasonTok, "cached_tokens": cachedTok, "total_tokens": totalTok, "cost": cost, "has_content": usageLogHasContent(db, id)}
				if !public {
					item["api_key"] = apiKey
					item["api_key_name"] = apiKeyName
					item["source"] = source
					item["channel_name"] = channel
					item["auth_index"] = authIdx
				}
				items = append(items, item)
			}
		}
	}

	payload := gin.H{"items": items, "total": totalStats.Total, "page": filters.Page, "size": filters.Size, "filters": usageLogFilters(db, public), "stats": gin.H{"total": totalStats.Total, "success_rate": successRate, "total_tokens": totalStats.TotalTokens, "total_cost": totalStats.TotalCost}}
	return payload
}

func usageLogFilters(db *sql.DB, public bool) gin.H {
	cols := requestLogColumns(db)
	models := []string{}
	if cols["model"] {
		models = distinctStringValues(db, "request_logs", "model", "model != ''", nil)
	}
	filters := gin.H{"models": models}
	if public {
		return filters
	}
	apiKeys := []string{}
	if cols["api_key"] {
		apiKeys = distinctStringValues(db, "request_logs", "api_key", "api_key != ''", nil)
	}
	apiKeyNames := gin.H{}
	if cols["api_key"] {
		joinSQL, keyExpr, nameExpr := usageAPIKeyNameLookup(db, cols)
		rows, err := db.Query("SELECT " + keyExpr + ", " + nameExpr + " FROM request_logs" + joinSQL + " WHERE coalesce(" + keyExpr + ", '') != '' AND coalesce(" + nameExpr + ", '') != '' GROUP BY " + keyExpr + ", " + nameExpr + " ORDER BY " + nameExpr + ", " + keyExpr + " LIMIT 500")
		if err == nil && rows != nil {
			defer rows.Close()
			for rows.Next() {
				var key, name string
				if rows.Scan(&key, &name) == nil {
					apiKeyNames[key] = name
				}
			}
		}
	}
	filters["api_keys"] = apiKeys
	filters["api_key_names"] = apiKeyNames
	channels := []string{}
	if cols["channel_name"] {
		channels = distinctStringValues(db, "request_logs", "channel_name", "channel_name != ''", nil)
	}
	filters["channels"] = channels
	return filters
}

func distinctStringValues(db *sql.DB, table, expr, where string, args []any) []string {
	if !dbTableExists(db, table) {
		return []string{}
	}
	rows, err := db.Query("SELECT DISTINCT "+expr+" FROM "+table+" WHERE "+where+" ORDER BY 1 LIMIT 200", args...)
	if err != nil || rows == nil {
		return []string{}
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var value string
		if rows.Scan(&value) == nil && strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func writeUsageLogContent(c *gin.Context, db *sql.DB, id int, apiKey, part string) {
	model, timestamp, exists := usageLogIdentity(db, id, apiKey)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "log not found"})
		return
	}
	inputContent, outputContent, detailContent := usageLogContent(db, id)
	part = strings.ToLower(strings.TrimSpace(part))
	if part == "input" || part == "output" || part == "details" {
		content := inputContent
		if part == "output" {
			content = outputContent
		}
		if part == "details" {
			content = detailContent
		}
		c.JSON(http.StatusOK, gin.H{"id": id, "model": model, "timestamp": timestamp, "part": part, "content": content})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "model": model, "timestamp": timestamp, "input_content": inputContent, "output_content": outputContent, "detail_content": detailContent})
}

func usageLogIdentity(db *sql.DB, id int, apiKey string) (string, string, bool) {
	if !dbTableExists(db, "request_logs") {
		return "", "", false
	}
	cols := requestLogColumns(db)
	where := "id = ?"
	args := []any{id}
	if strings.TrimSpace(apiKey) != "" && cols["api_key"] {
		where += " AND api_key = ?"
		args = append(args, strings.TrimSpace(apiKey))
	}
	var model, timestamp string
	err := db.QueryRow("SELECT "+usageColumnExpr(cols, "model", "''")+", "+usageColumnExpr(cols, "timestamp", "''")+" FROM request_logs WHERE "+where, args...).Scan(&model, &timestamp)
	return model, timestamp, err == nil
}

func usageLogContent(db *sql.DB, id int) (string, string, string) {
	var inputContent, outputContent, detailContent string
	if dbTableExists(db, "request_log_content") {
		cols := tableColumns(db, "request_log_content")
		row := db.QueryRow("SELECT "+usageColumnExpr(cols, "input_content", "''")+", "+usageColumnExpr(cols, "output_content", "''")+", "+usageColumnExpr(cols, "detail_content", "''")+" FROM request_log_content WHERE "+usageColumnExpr(cols, "log_id", "0")+" = ?", id)
		_ = row.Scan(&inputContent, &outputContent, &detailContent)
	}
	if inputContent == "" && outputContent == "" {
		cols := requestLogColumns(db)
		if cols["input_content"] || cols["output_content"] {
			row := db.QueryRow("SELECT "+usageColumnExpr(cols, "input_content", "''")+", "+usageColumnExpr(cols, "output_content", "''")+" FROM request_logs WHERE id = ?", id)
			_ = row.Scan(&inputContent, &outputContent)
		}
	}
	return inputContent, outputContent, detailContent
}

func usageLogHasContent(db *sql.DB, id int64) bool {
	if dbTableExists(db, "request_log_content") {
		cols := tableColumns(db, "request_log_content")
		var count int64
		_ = db.QueryRow("SELECT count(*) FROM request_log_content WHERE "+usageColumnExpr(cols, "log_id", "0")+" = ? AND (length(coalesce("+usageColumnExpr(cols, "input_content", "''")+", '')) > 0 OR length(coalesce("+usageColumnExpr(cols, "output_content", "''")+", '')) > 0 OR length(coalesce("+usageColumnExpr(cols, "detail_content", "''")+", '')) > 0)", id).Scan(&count)
		if count > 0 {
			return true
		}
	}
	cols := requestLogColumns(db)
	if cols["input_content"] || cols["output_content"] {
		var count int64
		_ = db.QueryRow("SELECT count(*) FROM request_logs WHERE id = ? AND (length(coalesce("+usageColumnExpr(cols, "input_content", "''")+", '')) > 0 OR length(coalesce("+usageColumnExpr(cols, "output_content", "''")+", '')) > 0)", id).Scan(&count)
		return count > 0
	}
	return false
}

func clearUsageLogData(db *sql.DB, clearBody, clearDetail, clearRecords bool) gin.H {
	out := gin.H{"deleted_logs": 0, "deleted_contents": 0, "cleared_body_rows": 0, "cleared_detail_rows": 0, "cleared_legacy_rows": 0}
	if clearRecords {
		out["deleted_contents"] = countRows(db, "request_log_content")
		out["deleted_logs"] = countRows(db, "request_logs")
		if dbTableExists(db, "request_log_content") {
			_, _ = db.Exec("DELETE FROM request_log_content")
		}
		if dbTableExists(db, "request_logs") {
			_, _ = db.Exec("DELETE FROM request_logs")
		}
		return out
	}
	if dbTableExists(db, "request_log_content") {
		cols := tableColumns(db, "request_log_content")
		if clearBody && (cols["input_content"] || cols["output_content"]) {
			out["cleared_body_rows"] = countRowsWhere(db, "request_log_content", "length(coalesce("+usageColumnExpr(cols, "input_content", "''")+", '')) > 0 OR length(coalesce("+usageColumnExpr(cols, "output_content", "''")+", '')) > 0")
			sets := []string{}
			if cols["input_content"] {
				sets = append(sets, "input_content = ''")
			}
			if cols["output_content"] {
				sets = append(sets, "output_content = ''")
			}
			_, _ = db.Exec("UPDATE request_log_content SET " + strings.Join(sets, ", "))
		}
		if clearDetail && cols["detail_content"] {
			out["cleared_detail_rows"] = countRowsWhere(db, "request_log_content", "length(coalesce(detail_content, '')) > 0")
			_, _ = db.Exec("UPDATE request_log_content SET detail_content = ''")
		}
	}
	if clearBody && dbTableExists(db, "request_logs") {
		cols := requestLogColumns(db)
		if cols["input_content"] || cols["output_content"] {
			out["cleared_legacy_rows"] = countRowsWhere(db, "request_logs", "length(coalesce("+usageColumnExpr(cols, "input_content", "''")+", '')) > 0 OR length(coalesce("+usageColumnExpr(cols, "output_content", "''")+", '')) > 0")
			sets := []string{}
			if cols["input_content"] {
				sets = append(sets, "input_content = ''")
			}
			if cols["output_content"] {
				sets = append(sets, "output_content = ''")
			}
			_, _ = db.Exec("UPDATE request_logs SET " + strings.Join(sets, ", "))
		}
	}
	return out
}

func publicUsageSummaryPayload(apiKey string, totals usageTotals, found bool) gin.H {
	successRate := float64(0)
	if totals.Total > 0 {
		successRate = float64(totals.Success) / float64(totals.Total) * 100
	}
	item := gin.H{
		"total_requests":   totals.Total,
		"success_count":    totals.Success,
		"failure_count":    totals.Failed,
		"success_rate":     successRate,
		"input_tokens":     totals.InputTokens,
		"output_tokens":    totals.OutputTokens,
		"reasoning_tokens": totals.ReasoningTokens,
		"cached_tokens":    totals.CachedTokens,
		"total_tokens":     totals.TotalTokens,
		"total_cost":       totals.TotalCost,
	}
	return gin.H{
		"found": found,
		"usage": gin.H{
			"total_requests": totals.Total,
			"success_count":  totals.Success,
			"failure_count":  totals.Failed,
			"total_tokens":   totals.TotalTokens,
			"total_cost":     totals.TotalCost,
			"apis":           gin.H{apiKey: item},
		},
	}
}

func usageAPIKeyExists(db *sql.DB, apiKey string) bool {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return false
	}
	if dbTableExists(db, "api_keys") {
		var count int64
		_ = db.QueryRow("SELECT count(*) FROM api_keys WHERE key = ?", apiKey).Scan(&count)
		if count > 0 {
			return true
		}
	}
	if dbTableExists(db, "request_logs") {
		cols := requestLogColumns(db)
		if cols["api_key"] {
			var count int64
			_ = db.QueryRow("SELECT count(*) FROM request_logs WHERE api_key = ? LIMIT 1", apiKey).Scan(&count)
			return count > 0
		}
	}
	return false
}

func countRows(db *sql.DB, table string) int64 {
	if !dbTableExists(db, table) {
		return 0
	}
	var count int64
	_ = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count)
	return count
}

func countRowsWhere(db *sql.DB, table, where string) int64 {
	if !dbTableExists(db, table) {
		return 0
	}
	var count int64
	_ = db.QueryRow("SELECT count(*) FROM " + table + " WHERE " + where).Scan(&count)
	return count
}

func requestLogColumns(db *sql.DB) map[string]bool {
	return tableColumns(db, "request_logs")
}

func tableColumns(db *sql.DB, table string) map[string]bool {
	out := map[string]bool{}
	if strings.TrimSpace(table) == "" || strings.ContainsAny(table, "\"'`;() \t\r\n") {
		return out
	}
	rows, err := db.Query("pragma table_info(" + table + ")")
	if err != nil {
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if errScan := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); errScan == nil {
			out[name] = true
		}
	}
	return out
}

func dbTableExists(db *sql.DB, table string) bool {
	if strings.TrimSpace(table) == "" || strings.ContainsAny(table, "\"'`;() \t\r\n") {
		return false
	}
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=? LIMIT 1", table).Scan(&name)
	return err == nil
}

func usageColumnExpr(cols map[string]bool, column, fallback string) string {
	if cols[column] {
		return column
	}
	return fallback
}

func usageRequestLogColumnExpr(cols map[string]bool, column, fallback string) string {
	if cols[column] {
		return "request_logs." + column
	}
	return fallback
}

func usageAPIKeyNameLookup(db *sql.DB, requestLogCols map[string]bool) (string, string, string) {
	keyExpr := usageRequestLogColumnExpr(requestLogCols, "api_key", "''")
	logNameExpr := usageRequestLogColumnExpr(requestLogCols, "api_key_name", "''")
	nameExpr := "coalesce(nullif(" + logNameExpr + ", ''), '')"
	if !requestLogCols["api_key"] || !dbTableHasColumns(db, "api_keys", "key", "name") {
		return "", keyExpr, nameExpr
	}
	return " LEFT JOIN api_keys ON api_keys.key = request_logs.api_key", keyExpr, "coalesce(nullif(api_keys.name, ''), nullif(" + logNameExpr + ", ''), '')"
}

func dbTableHasColumns(db *sql.DB, table string, columns ...string) bool {
	if !dbTableExists(db, table) {
		return false
	}
	available := tableColumns(db, table)
	for _, column := range columns {
		if !available[column] {
			return false
		}
	}
	return true
}

func (h *Handler) updateConfigAPIKeys(keys []string) {
	if h == nil || h.cfg == nil {
		return
	}
	h.mu.Lock()
	h.cfg.APIKeys = keys
	h.mu.Unlock()
}

func (h *Handler) apiKeysDBPath() string {
	if h == nil || strings.TrimSpace(h.configFilePath) == "" {
		return ""
	}
	path := filepath.Join(filepath.Dir(h.configFilePath), "data", "usage.db")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

func (h *Handler) openAPIKeysDB() (*sql.DB, bool) {
	path := h.apiKeysDBPath()
	if path == "" {
		return nil, false
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, false
	}
	return db, true
}

func (h *Handler) apiKeyEntriesFromDB() ([]panelAPIKeyEntry, bool) {
	db, ok := h.openAPIKeysDB()
	if !ok {
		return nil, false
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("select 1 from api_keys limit 1"); err != nil {
		return nil, false
	}
	usage.EnsureAPIKeysTable(db)
	selectPermissionProfile := "''"
	if apiKeysDBHasColumn(db, "permission_profile_id") {
		selectPermissionProfile = "permission_profile_id"
	}
	rows, err := db.Query(`select key, name, disabled, daily_limit, total_quota, spending_limit, concurrency_limit, rpm_limit, tpm_limit, allowed_models, allowed_channels, allowed_channel_groups, system_prompt, created_at, ` + selectPermissionProfile + ` from api_keys order by created_at, key`)
	if err != nil {
		return nil, false
	}
	defer func() { _ = rows.Close() }()

	entries := make([]panelAPIKeyEntry, 0)
	for rows.Next() {
		var entry panelAPIKeyEntry
		var disabled int
		var modelsRaw, channelsRaw, groupsRaw string
		if errScan := rows.Scan(
			&entry.Key,
			&entry.Name,
			&disabled,
			&entry.DailyLimit,
			&entry.TotalQuota,
			&entry.SpendingLimit,
			&entry.ConcurrencyLimit,
			&entry.RPMLimit,
			&entry.TPMLimit,
			&modelsRaw,
			&channelsRaw,
			&groupsRaw,
			&entry.SystemPrompt,
			&entry.CreatedAt,
			&entry.PermissionProfileID,
		); errScan != nil {
			return nil, false
		}
		entry.Key = strings.TrimSpace(entry.Key)
		if entry.Key == "" {
			continue
		}
		entry.Disabled = disabled != 0
		entry.AllowedModels = stringSliceFromJSON(modelsRaw)
		entry.AllowedChannels = stringSliceFromJSON(channelsRaw)
		entry.AllowedChannelGroups = stringSliceFromJSON(groupsRaw)
		entries = append(entries, entry)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, false
	}
	return entries, true
}

func (h *Handler) replaceAPIKeyEntriesInDB(entries []panelAPIKeyEntry) error {
	db, ok := h.openAPIKeysDB()
	if !ok {
		return nil
	}
	if hasMaskedAPIKeyEntry(entries) {
		return fmt.Errorf("masked api key entry cannot be persisted")
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`create table if not exists api_keys (
		key text not null primary key,
		name text not null default '',
		disabled integer not null default 0,
		daily_limit integer not null default 0,
		total_quota integer not null default 0,
		spending_limit real not null default 0,
		concurrency_limit integer not null default 0,
		rpm_limit integer not null default 0,
		tpm_limit integer not null default 0,
		allowed_models text not null default '[]',
		allowed_channels text not null default '[]',
		allowed_channel_groups text not null default '[]',
		system_prompt text not null default '',
		created_at text not null default '',
		updated_at text not null default '',
		permission_profile_id text not null default ''
	)`); err != nil {
		return err
	}
	usage.EnsureAPIKeysTable(db)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec("delete from api_keys"); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`insert into api_keys (
		key, name, disabled, daily_limit, total_quota, spending_limit, concurrency_limit, rpm_limit, tpm_limit,
		allowed_models, allowed_channels, allowed_channel_groups, system_prompt, created_at, updated_at, permission_profile_id
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	now := time.Now().UTC().Format(time.RFC3339)
	seen := map[string]struct{}{}
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		createdAt := strings.TrimSpace(entry.CreatedAt)
		if createdAt == "" {
			createdAt = now
		}
		disabled := 0
		if entry.Disabled {
			disabled = 1
		}
		if _, err = stmt.Exec(
			key,
			strings.TrimSpace(entry.Name),
			disabled,
			entry.DailyLimit,
			entry.TotalQuota,
			entry.SpendingLimit,
			entry.ConcurrencyLimit,
			entry.RPMLimit,
			entry.TPMLimit,
			stringSliceJSON(entry.AllowedModels),
			stringSliceJSON(entry.AllowedChannels),
			stringSliceJSON(entry.AllowedChannelGroups),
			strings.TrimSpace(entry.SystemPrompt),
			createdAt,
			now,
			strings.TrimSpace(entry.PermissionProfileID),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func apiKeysDBHasColumn(db *sql.DB, column string) bool {
	rows, err := db.Query("pragma table_info(api_keys)")
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if errScan := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); errScan != nil {
			return false
		}
		if strings.EqualFold(name, column) {
			return true
		}
	}
	return false
}

func stringSliceFromJSON(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &values); err != nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func stringSliceJSON(values []string) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func readAPIKeyEntries(c *gin.Context) ([]panelAPIKeyEntry, bool) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return nil, false
	}
	var entries []panelAPIKeyEntry
	if err = json.Unmarshal(data, &entries); err == nil {
		return entries, true
	}
	var wrapped struct {
		Entries []panelAPIKeyEntry `json:"api-key-entries"`
		Items   []panelAPIKeyEntry `json:"items"`
	}
	if err = json.Unmarshal(data, &wrapped); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return nil, false
	}
	if wrapped.Entries != nil {
		return wrapped.Entries, true
	}
	return wrapped.Items, true
}

func entriesFromKeys(keys []string) []panelAPIKeyEntry {
	entries := make([]panelAPIKeyEntry, 0, len(keys))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		entries = append(entries, panelAPIKeyEntry{Key: key, CreatedAt: now})
	}
	return entries
}

func keysFromEntries(entries []panelAPIKeyEntry) []string {
	keys := make([]string, 0, len(entries))
	seen := map[string]struct{}{}
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func allStaticModelInfos() []*registry.ModelInfo {
	models := make([]*registry.ModelInfo, 0)
	models = append(models, registry.GetClaudeModels()...)
	models = append(models, registry.GetGeminiModels()...)
	models = append(models, registry.GetGeminiVertexModels()...)
	models = append(models, registry.GetGeminiCLIModels()...)
	models = append(models, registry.GetAIStudioModels()...)
	models = append(models, registry.GetCodexProModels()...)
	models = append(models, registry.GetKimiModels()...)
	models = append(models, registry.GetAntigravityModels()...)
	sort.Slice(models, func(i, j int) bool {
		return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
	})
	return models
}

func modelMapsFromInfos(models []*registry.ModelInfo, enabled bool, source string) []map[string]any {
	out := make([]map[string]any, 0, len(models))
	seen := map[string]struct{}{}
	for _, model := range models {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(model.ID))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, map[string]any{
			"id":          model.ID,
			"object":      firstNonEmpty(model.Object, "model"),
			"owned_by":    firstNonEmpty(model.OwnedBy, model.Type),
			"description": firstNonEmpty(model.Description, model.DisplayName),
			"enabled":     enabled,
			"source":      source,
		})
	}
	return out
}

func positiveQueryInt(c *gin.Context, key string, fallback int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func normalizeOwner(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "-")
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func unsupportedPanelWrite(c *gin.Context, message string) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": "unsupported", "message": message})
}

func (h *Handler) currentAPIKeyEntries() ([]panelAPIKeyEntry, bool) {
	entries, dbOK := h.apiKeyEntriesFromDB()
	if h != nil && h.cfg != nil {
		if dbOK {
			repaired, changed := repairMaskedAPIKeyEntries(entries, h.cfg.APIKeys)
			entries = repaired
			if changed && !hasMaskedAPIKeyEntry(entries) {
				_ = h.replaceAPIKeyEntriesInDB(entries)
			}
		} else {
			entries = entriesFromKeys(h.cfg.APIKeys)
		}
	}
	return entries, dbOK
}

func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "sk-***" + key[len(key)-3:]
	}
	return "sk-***" + key[len(key)-4:]
}

func isMaskedAPIKey(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), "sk-***")
}

func hasMaskedAPIKeyEntry(entries []panelAPIKeyEntry) bool {
	for _, entry := range entries {
		if isMaskedAPIKey(entry.Key) {
			return true
		}
	}
	return false
}

func rejectMaskedAPIKeyEntries(c *gin.Context, entries []panelAPIKeyEntry) bool {
	if !hasMaskedAPIKeyEntry(entries) {
		return false
	}
	c.JSON(http.StatusBadRequest, gin.H{
		"error": "masked api key cannot be saved; reload the page or enter the full key",
	})
	return true
}

func preserveMaskedAPIKeys(entries, existing []panelAPIKeyEntry) []panelAPIKeyEntry {
	if len(entries) == 0 {
		return entries
	}
	out := make([]panelAPIKeyEntry, len(entries))
	copy(out, entries)
	for i := range out {
		key := strings.TrimSpace(out[i].Key)
		if key == "" || !isMaskedAPIKey(key) {
			continue
		}
		if i < len(existing) && maskAPIKey(existing[i].Key) == key {
			out[i].Key = strings.TrimSpace(existing[i].Key)
			continue
		}
		for _, candidate := range existing {
			if maskAPIKey(candidate.Key) == key {
				out[i].Key = strings.TrimSpace(candidate.Key)
				break
			}
		}
	}
	return out
}

func repairMaskedAPIKeyEntries(entries []panelAPIKeyEntry, configKeys []string) ([]panelAPIKeyEntry, bool) {
	if len(entries) == 0 || len(configKeys) == 0 {
		return entries, false
	}
	out := preserveMaskedAPIKeys(entries, entriesFromKeys(configKeys))
	changed := false
	for i := range entries {
		if strings.TrimSpace(entries[i].Key) != strings.TrimSpace(out[i].Key) {
			changed = true
			break
		}
	}
	return out, changed
}

func (h *Handler) refreshAccessProviders() {
	if h == nil || h.cfg == nil {
		return
	}
	configaccess.Register(&h.cfg.SDKConfig)
}
