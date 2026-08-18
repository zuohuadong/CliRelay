package management

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	MonthlySpendingLimit float64  `json:"monthly-spending-limit,omitempty"`
	BillingCycleAnchor   string   `json:"billing-cycle-anchor,omitempty"`
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
	if index := strings.TrimSpace(c.Query("index")); index != "" {
		idx, err := strconv.Atoi(index)
		if err != nil || idx < 0 || idx >= len(h.cfg.BedrockKey) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
			return
		}
		h.cfg.BedrockKey = append(h.cfg.BedrockKey[:idx], h.cfg.BedrockKey[idx+1:]...)
		h.cfg.SanitizeBedrockKeys()
		h.persistLocked(c)
		return
	}
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

func (h *Handler) PutModelOwnerPresets(c *gin.Context) {
	var body struct {
		Items []struct {
			Value       string `json:"value"`
			ID          string `json:"id"`
			Owner       string `json:"owner"`
			Label       string `json:"label"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Enabled     *bool  `json:"enabled"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	seen := map[string]struct{}{}
	items := make([]gin.H, 0, len(body.Items))
	for _, item := range body.Items {
		value := normalizeOwner(firstNonEmpty(item.Value, item.ID, item.Owner))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		label := strings.TrimSpace(firstNonEmpty(item.Label, item.Name, value))
		enabled := true
		if item.Enabled != nil {
			enabled = *item.Enabled
		}
		items = append(items, gin.H{
			"value":       value,
			"label":       label,
			"description": strings.TrimSpace(item.Description),
			"enabled":     enabled,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return fmt.Sprint(items[i]["label"]) < fmt.Sprint(items[j]["label"])
	})
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
	filters := usageFiltersFromQuery(c)
	requestContext := c.Request.Context()
	payload, err := h.loadUsageAggregate(requestContext, usageAggregateChart, filters, func(ctx context.Context) (gin.H, error) {
		db, ok := h.usageDB()
		if !ok {
			return nil, errUsageDatabaseUnavailable
		}
		defer func() { _ = db.Close() }()
		return buildUsageChartPayloadContext(ctx, db, filters, false)
	})
	if err != nil {
		if requestContext.Err() != nil {
			return
		}
		c.JSON(http.StatusOK, emptyUsageChartPayload())
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (h *Handler) GetUsageEntityStats(c *gin.Context) {
	filters := usageFiltersFromQuery(c)
	requestContext := c.Request.Context()
	payload, err := h.loadUsageAggregate(requestContext, usageAggregateEntity, filters, func(ctx context.Context) (gin.H, error) {
		db, ok := h.usageDB()
		if !ok {
			return nil, errUsageDatabaseUnavailable
		}
		defer func() { _ = db.Close() }()
		return h.buildUsageEntityStatsPayloadContext(ctx, db, filters)
	})
	if err != nil {
		if requestContext.Err() != nil {
			return
		}
		c.JSON(http.StatusOK, emptyUsageEntityStatsPayload())
		return
	}
	c.JSON(http.StatusOK, payload)
}

var errUsageDatabaseUnavailable = errors.New("usage database unavailable")

func emptyUsageEntityStatsPayload() gin.H {
	return gin.H{"source": []gin.H{}, "auth_index": []gin.H{}}
}

func (h *Handler) buildUsageEntityStatsPayloadContext(ctx context.Context, db *sql.DB, filters usageFilters) (gin.H, error) {
	sourceSeen := make(map[string]bool)
	sourceStats := make([]gin.H, 0)
	cols := requestLogColumns(db)
	sourceFilters := filters
	if len(sourceFilters.Sources) > 0 {
		sourceFilters.AuthIndexes = nil
	}
	sourceWhereSQL, sourceArgs := sourceFilters.whereClause(db)
	rows, err := db.QueryContext(ctx, "SELECT "+usageColumnExpr(cols, "source", "''")+", count(*), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"=0 then 1 else 0 end),0), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"!=0 then 1 else 0 end),0), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0), coalesce(avg("+usageColumnExpr(cols, "latency_ms", "0")+"),0) FROM request_logs WHERE "+sourceWhereSQL+" GROUP BY "+usageColumnExpr(cols, "source", "''")+" ORDER BY count(*) DESC LIMIT 100", sourceArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var source string
		var total, successCnt, failCnt, toks int64
		var avgLatency float64
		if rows.Scan(&source, &total, &successCnt, &failCnt, &toks, &avgLatency) == nil {
			sourceStats = append(sourceStats, gin.H{"entity_name": source, "source": source, "requests": total, "failed": failCnt, "tokens": toks, "total_tokens": toks, "avg_latency": avgLatency})
			sourceSeen[source] = true
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}

	authIndexToAPIKey := h.buildAuthIndexToAPIKeyMap()

	authStats := make([]gin.H, 0)
	authFilters := filters
	if len(authFilters.AuthIndexes) > 0 {
		authFilters.Sources = nil
	}
	authWhereSQL, authArgs := authFilters.whereClause(db)
	rows2, err := db.QueryContext(ctx, "SELECT "+usageColumnExpr(cols, "auth_index", "''")+", count(*), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"=0 then 1 else 0 end),0), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"!=0 then 1 else 0 end),0), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0), coalesce(avg("+usageColumnExpr(cols, "latency_ms", "0")+"),0) FROM request_logs WHERE "+authWhereSQL+" GROUP BY "+usageColumnExpr(cols, "auth_index", "''")+" ORDER BY count(*) DESC LIMIT 100", authArgs...)
	if err != nil {
		return nil, err
	}
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
	err = rows2.Err()
	_ = rows2.Close()
	if err != nil {
		return nil, err
	}

	return gin.H{"source": sourceStats, "auth_index": authStats}, nil
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

// collectAPIKeyBillingPayload gathers billing data for the given API key.
// Shared by both the management route (GetAPIKeyBilling) and the public
// self-service route (GetPublicAPIKeyBilling).
func (h *Handler) collectAPIKeyBillingPayload(apiKey string) gin.H {
	entry := panelAPIKeyEntry{Key: apiKey}
	if h != nil {
		if entries, _ := h.currentAPIKeyEntries(); len(entries) > 0 {
			for _, candidate := range entries {
				if strings.TrimSpace(candidate.Key) == apiKey {
					entry = candidate
					break
				}
			}
		}
	}

	now := time.Now().UTC()
	currentStart, currentEnd := usage.CurrentMonthlyBillingCycle(entry.BillingCycleAnchor, now)
	limit := entry.MonthlySpendingLimit

	db, ok := h.usageDB()
	if !ok {
		payload := apiKeyBillingPayload(apiKey, entry, limit, currentStart, currentEnd, usageTotals{}, nil)
		payload["model_breakdown"] = []gin.H{}
		if current, _ := payload["current_cycle"].(gin.H); current != nil {
			current["model_breakdown"] = []gin.H{}
		}
		return payload
	}
	defer func() { _ = db.Close() }()

	current := queryAPIKeyBillingTotals(db, apiKey, currentStart, currentEnd)
	modelBreakdown := queryAPIKeyBillingModelBreakdown(db, apiKey, currentStart, currentEnd)
	history := make([]gin.H, 0, 6)
	start := currentStart
	end := currentEnd
	for i := 0; i < 6; i++ {
		totals := queryAPIKeyBillingTotals(db, apiKey, start, end)
		history = append(history, apiKeyBillingCyclePayload(limit, start, end, totals))
		end = start
		start = start.AddDate(0, -1, 0)
	}

	payload := apiKeyBillingPayload(apiKey, entry, limit, currentStart, currentEnd, current, history)
	payload["model_breakdown"] = modelBreakdown
	if currentPayload, _ := payload["current_cycle"].(gin.H); currentPayload != nil {
		currentPayload["model_breakdown"] = modelBreakdown
	}
	return payload
}

func (h *Handler) GetAPIKeyBilling(c *gin.Context) {
	apiKey := strings.TrimSpace(c.Query("api_key"))
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing api_key"})
		return
	}

	payload := h.collectAPIKeyBillingPayload(apiKey)
	if wantsAPIKeyBillingHTML(c) {
		renderAPIKeyBillingHTML(c, payload)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (h *Handler) apiKeyBillingEntry(apiKey string) (panelAPIKeyEntry, bool) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || h == nil {
		return panelAPIKeyEntry{}, false
	}
	entries, _ := h.currentAPIKeyEntries()
	if len(entries) == 0 {
		return panelAPIKeyEntry{}, false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Key) == apiKey {
			return entry, true
		}
	}
	return panelAPIKeyEntry{}, false
}

// GetPublicAPIKeyBilling serves the self-service billing page that only
// requires the user's own api_key (no management token). By default it
// returns an Alpine.js-powered HTML page for browser visits; format=json
// falls back to the raw JSON payload.
func (h *Handler) GetPublicAPIKeyBilling(c *gin.Context) {
	apiKey := strings.TrimSpace(c.Query("api_key"))
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing api_key"})
		return
	}
	entry, found := h.apiKeyBillingEntry(apiKey)
	if !found {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid api key"})
		return
	}
	if entry.Disabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "api key disabled"})
		return
	}

	payload := h.collectAPIKeyBillingPayload(apiKey)
	if wantsAPIKeyBillingHTML(c) {
		renderAPIKeyBillingAlpineHTML(c, payload)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func apiKeyBillingPayload(apiKey string, entry panelAPIKeyEntry, limit float64, currentStart, currentEnd time.Time, current usageTotals, history []gin.H) gin.H {
	if history == nil {
		history = []gin.H{}
	}
	return gin.H{
		"api_key":                  apiKey,
		"name":                     entry.Name,
		"monthly_spending_limit":   limit,
		"billing_cycle_anchor":     strings.TrimSpace(entry.BillingCycleAnchor),
		"current_cycle":            apiKeyBillingCyclePayload(limit, currentStart, currentEnd, current),
		"history":                  history,
		"billing_cycle_mode":       "monthly",
		"billing_cycle_owner":      "api_key",
		"billing_cycle_owner_note": "This v7 compatibility build stores the billing anchor on the API key entry.",
	}
}

func apiKeyBillingCyclePayload(limit float64, start, end time.Time, totals usageTotals) gin.H {
	remaining := 0.0
	if limit > 0 {
		remaining = limit - totals.TotalCost
		if remaining < 0 {
			remaining = 0
		}
	}
	return gin.H{
		"start":         start.UTC().Format(time.RFC3339),
		"end":           end.UTC().Format(time.RFC3339),
		"request_count": totals.Total,
		"success_count": totals.Success,
		"failed_count":  totals.Failed,
		"total_tokens":  totals.TotalTokens,
		"total_cost":    totals.TotalCost,
		"limit":         limit,
		"remaining":     remaining,
		"exceeded":      limit > 0 && totals.TotalCost >= limit,
	}
}

func queryAPIKeyBillingTotals(db *sql.DB, apiKey string, start, end time.Time) usageTotals {
	if db == nil || !dbTableExists(db, "request_logs") {
		return usageTotals{}
	}
	cols := requestLogColumns(db)
	if !cols["api_key"] || !cols["timestamp"] {
		return usageTotals{}
	}
	row := db.QueryRow(`
		SELECT count(*),
		       coalesce(sum(case when `+usageColumnExpr(cols, "failed", "0")+`=0 then 1 else 0 end),0),
		       coalesce(sum(case when `+usageColumnExpr(cols, "failed", "0")+`!=0 then 1 else 0 end),0),
		       coalesce(sum(`+usageColumnExpr(cols, "total_tokens", "0")+`),0),
		       coalesce(sum(case when `+usageColumnExpr(cols, "failed", "0")+`=0 then `+usageColumnExpr(cols, "cost", "0")+` else 0 end),0)
		FROM request_logs
		WHERE api_key = ? AND timestamp >= ? AND timestamp < ?`,
		apiKey, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano),
	)
	var totals usageTotals
	_ = row.Scan(&totals.Total, &totals.Success, &totals.Failed, &totals.TotalTokens, &totals.TotalCost)
	return totals
}

func queryAPIKeyBillingModelBreakdown(db *sql.DB, apiKey string, start, end time.Time) []gin.H {
	if db == nil || !dbTableExists(db, "request_logs") {
		return []gin.H{}
	}
	cols := requestLogColumns(db)
	if !cols["api_key"] || !cols["timestamp"] || !cols["model"] {
		return []gin.H{}
	}
	rows, err := db.Query(`
		SELECT `+usageColumnExpr(cols, "model", "''")+`,
		       count(*),
		       coalesce(sum(case when `+usageColumnExpr(cols, "failed", "0")+`=0 then 1 else 0 end),0),
		       coalesce(sum(case when `+usageColumnExpr(cols, "failed", "0")+`!=0 then 1 else 0 end),0),
		       coalesce(sum(`+usageColumnExpr(cols, "input_tokens", "0")+`),0),
		       coalesce(sum(`+usageColumnExpr(cols, "output_tokens", "0")+`),0),
		       coalesce(sum(`+usageColumnExpr(cols, "reasoning_tokens", "0")+`),0),
		       coalesce(sum(`+usageColumnExpr(cols, "cached_tokens", "0")+`),0),
		       coalesce(sum(`+usageColumnExpr(cols, "total_tokens", "0")+`),0),
		       coalesce(sum(case when `+usageColumnExpr(cols, "failed", "0")+`=0 then `+usageColumnExpr(cols, "cost", "0")+` else 0 end),0),
		       coalesce(sum(case when `+usageColumnExpr(cols, "failed", "0")+`=0 then `+usageColumnExpr(cols, "input_tokens", "0")+` else 0 end),0),
		       coalesce(sum(case when `+usageColumnExpr(cols, "failed", "0")+`=0 then `+usageColumnExpr(cols, "output_tokens", "0")+` else 0 end),0),
		       coalesce(sum(case when `+usageColumnExpr(cols, "failed", "0")+`=0 then `+usageColumnExpr(cols, "reasoning_tokens", "0")+` else 0 end),0),
		       coalesce(sum(case when `+usageColumnExpr(cols, "failed", "0")+`=0 then `+usageColumnExpr(cols, "cached_tokens", "0")+` else 0 end),0)
		FROM request_logs
		WHERE api_key = ? AND timestamp >= ? AND timestamp < ?
		GROUP BY `+usageColumnExpr(cols, "model", "''")+`
		ORDER BY 10 DESC, 9 DESC, 2 DESC
		LIMIT 50`,
		apiKey, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return []gin.H{}
	}
	defer func() { _ = rows.Close() }()

	type modelRow struct {
		payload gin.H
	}
	items := make([]modelRow, 0)
	var totalRequests, totalTokens int64
	var totalCost float64
	for rows.Next() {
		var model string
		var requests, success, failed, input, output, reasoning, cached, tokens int64
		var billedInput, billedOutput, billedReasoning, billedCached int64
		var actualCost float64
		if errScan := rows.Scan(&model, &requests, &success, &failed, &input, &output, &reasoning, &cached, &tokens, &actualCost, &billedInput, &billedOutput, &billedReasoning, &billedCached); errScan != nil {
			continue
		}
		model = strings.TrimSpace(model)
		if model == "" {
			model = "unknown"
		}
		price, hasPrice := apiKeyBillingModelPrice(db, model)
		listCost := apiKeyBillingListCost(price, success, billedInput, billedOutput, billedReasoning, billedCached)
		discountRate := 0.0
		billingMultiplier := 0.0
		if listCost > 0 {
			billingMultiplier = actualCost / listCost
			discountRate = 1 - billingMultiplier
		}
		item := gin.H{
			"model":                    model,
			"request_count":            requests,
			"success_count":            success,
			"failed_count":             failed,
			"input_tokens":             input,
			"output_tokens":            output,
			"reasoning_tokens":         reasoning,
			"cached_tokens":            cached,
			"total_tokens":             tokens,
			"total_cost":               actualCost,
			"estimated_list_cost":      listCost,
			"billing_multiplier":       billingMultiplier,
			"discount_rate":            discountRate,
			"discount_percent":         discountRate * 100,
			"has_price":                hasPrice,
			"price_mode":               price.Mode,
			"input_price_per_million":  price.InputPricePerM,
			"output_price_per_million": price.OutputPricePerM,
			"cached_price_per_million": price.CachedPricePerM,
			"price_per_call":           price.PricePerCall,
		}
		items = append(items, modelRow{payload: item})
		totalRequests += requests
		totalTokens += tokens
		totalCost += actualCost
	}
	if errRows := rows.Err(); errRows != nil {
		return []gin.H{}
	}
	out := make([]gin.H, 0, len(items))
	for _, item := range items {
		if totalRequests > 0 {
			item.payload["request_share"] = float64(billingInt(item.payload, "request_count")) / float64(totalRequests)
		} else {
			item.payload["request_share"] = 0.0
		}
		if totalTokens > 0 {
			item.payload["token_share"] = float64(billingInt(item.payload, "total_tokens")) / float64(totalTokens)
		} else {
			item.payload["token_share"] = 0.0
		}
		if totalCost > 0 {
			item.payload["cost_share"] = billingFloat(item.payload, "total_cost") / totalCost
		} else {
			item.payload["cost_share"] = 0.0
		}
		out = append(out, item.payload)
	}
	return out
}

func apiKeyBillingModelPrice(db *sql.DB, model string) (usage.ModelPriceRow, bool) {
	if db == nil || !dbTableExists(db, "model_prices") {
		return usage.ModelPriceRow{Model: strings.TrimSpace(model), Mode: "token"}, false
	}
	var row usage.ModelPriceRow
	err := db.QueryRow(`
		SELECT model, mode, input_price_per_m, output_price_per_m, cached_price_per_m, price_per_call, updated_at
		FROM model_prices
		WHERE model = ?`,
		strings.TrimSpace(model),
	).Scan(&row.Model, &row.Mode, &row.InputPricePerM, &row.OutputPricePerM, &row.CachedPricePerM, &row.PricePerCall, &row.UpdatedAt)
	if err != nil {
		return usage.ModelPriceRow{Model: strings.TrimSpace(model), Mode: "token"}, false
	}
	if strings.TrimSpace(row.Mode) == "" {
		row.Mode = "token"
	}
	return row, true
}

func apiKeyBillingListCost(price usage.ModelPriceRow, success, inputTokens, outputTokens, reasoningTokens, cachedTokens int64) float64 {
	if price.Mode == "call" && price.PricePerCall > 0 {
		if success < 0 {
			success = 0
		}
		return float64(success) * price.PricePerCall
	}
	netInput := inputTokens - cachedTokens
	if netInput < 0 {
		netInput = inputTokens
	}
	inputCost := float64(netInput) / 1_000_000 * price.InputPricePerM
	outputCost := float64(outputTokens+reasoningTokens) / 1_000_000 * price.OutputPricePerM
	cachedCost := float64(cachedTokens) / 1_000_000 * price.CachedPricePerM
	total := inputCost + outputCost + cachedCost
	if total < 0 {
		return 0
	}
	return total
}

func wantsAPIKeyBillingHTML(c *gin.Context) bool {
	if c == nil || c.Query("format") == "json" {
		return false
	}
	if c.Query("format") == "html" {
		return true
	}
	accept := strings.ToLower(strings.TrimSpace(c.GetHeader("Accept")))
	if accept == "" {
		return false
	}
	htmlIndex := strings.Index(accept, "text/html")
	if htmlIndex < 0 {
		return false
	}
	jsonIndex := strings.Index(accept, "application/json")
	return jsonIndex < 0 || htmlIndex < jsonIndex
}

func renderAPIKeyBillingHTML(c *gin.Context, payload gin.H) {
	apiKey, _ := payload["api_key"].(string)
	name, _ := payload["name"].(string)
	anchor, _ := payload["billing_cycle_anchor"].(string)
	current, _ := payload["current_cycle"].(gin.H)
	history, _ := payload["history"].([]gin.H)
	if current == nil {
		current = gin.H{}
	}

	title := "API Key 账单"
	displayName := strings.TrimSpace(name)
	if displayName == "" {
		displayName = "未命名 API Key"
	}
	maskedKey := maskAPIKey(apiKey)
	if maskedKey == "" {
		maskedKey = "sk-***"
	}

	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, buildAPIKeyBillingHTML(title, displayName, maskedKey, anchor, current, history))
}

func buildAPIKeyBillingHTML(title, displayName, maskedKey, anchor string, current gin.H, history []gin.H) string {
	used := billingFloat(current, "total_cost")
	limit := billingFloat(current, "limit")
	remaining := billingFloat(current, "remaining")
	requests := billingInt(current, "request_count")
	success := billingInt(current, "success_count")
	failed := billingInt(current, "failed_count")
	tokens := billingInt(current, "total_tokens")
	exceeded, _ := current["exceeded"].(bool)
	statusText := "正常"
	statusClass := "ok"
	if exceeded {
		statusText = "已超限"
		statusClass = "danger"
	}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`</title><style>
:root{color-scheme:light dark;--bg:#f5f7fb;--panel:#ffffff;--panel2:#f8fafc;--text:#111827;--muted:#64748b;--line:#e2e8f0;--accent:#0f766e;--accent2:#14b8a6;--danger:#dc2626;--ok:#16a34a;--shadow:0 24px 70px rgba(15,23,42,.12)}
@media(prefers-color-scheme:dark){:root{--bg:#0b1120;--panel:#111827;--panel2:#172033;--text:#f8fafc;--muted:#94a3b8;--line:#263244;--shadow:0 24px 70px rgba(0,0,0,.38)}}
*{box-sizing:border-box}body{margin:0;min-height:100vh;background:radial-gradient(circle at 15% 10%,rgba(20,184,166,.20),transparent 34%),linear-gradient(135deg,var(--bg),#e8eef7);color:var(--text);font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
.wrap{width:min(1120px,calc(100vw - 32px));margin:0 auto;padding:48px 0}.hero{display:flex;justify-content:space-between;gap:24px;align-items:flex-end;margin-bottom:22px}.eyebrow{margin:0 0 8px;color:var(--accent);font-size:12px;font-weight:800;letter-spacing:.18em;text-transform:uppercase}.title{margin:0;font-size:clamp(30px,5vw,52px);line-height:1.02;letter-spacing:-.04em}.sub{margin:14px 0 0;color:var(--muted);font-size:15px}.badge{display:inline-flex;align-items:center;gap:8px;border:1px solid var(--line);background:rgba(255,255,255,.52);border-radius:999px;padding:10px 14px;color:var(--muted);font-weight:700}.badge span{color:var(--text);font-family:ui-monospace,SFMono-Regular,Menlo,monospace}.panel{background:color-mix(in srgb,var(--panel) 92%,transparent);border:1px solid var(--line);border-radius:28px;box-shadow:var(--shadow);overflow:hidden;backdrop-filter:blur(18px)}.summary{display:grid;grid-template-columns:1.15fr repeat(3,1fr);gap:1px;background:var(--line)}.metric{background:var(--panel);padding:24px}.metric .label{color:var(--muted);font-size:12px;font-weight:800;letter-spacing:.12em;text-transform:uppercase}.metric .value{margin-top:12px;font-size:30px;font-weight:850;letter-spacing:-.03em}.metric .hint{margin-top:8px;color:var(--muted);font-size:13px}.metric.hero-metric{background:linear-gradient(135deg,rgba(15,118,110,.14),rgba(20,184,166,.05)),var(--panel)}.status{display:inline-flex;border-radius:999px;padding:5px 10px;font-size:12px;font-weight:800}.status.ok{background:rgba(22,163,74,.12);color:var(--ok)}.status.danger{background:rgba(220,38,38,.12);color:var(--danger)}.bar{height:12px;border-radius:999px;background:var(--panel2);overflow:hidden;margin-top:18px}.fill{height:100%;width:0;background:linear-gradient(90deg,var(--accent),var(--accent2));border-radius:inherit}.section{padding:24px}.section-head{display:flex;justify-content:space-between;gap:16px;align-items:center;margin-bottom:12px}.section h2{margin:0;font-size:18px}.table-wrap{overflow-x:auto;border:1px solid var(--line);border-radius:18px}table{width:100%;min-width:760px;border-collapse:collapse;background:var(--panel)}th,td{padding:14px 16px;text-align:left;border-bottom:1px solid var(--line);white-space:nowrap}th{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.12em}td{font-size:14px}tr:last-child td{border-bottom:0}.empty{padding:28px;border:1px dashed var(--line);border-radius:18px;color:var(--muted);background:var(--panel2)}.foot{margin-top:16px;color:var(--muted);font-size:12px}@media(max-width:820px){.wrap{padding:28px 0}.hero{display:block}.badge{margin-top:18px}.summary{grid-template-columns:1fr}.metric{padding:20px}.section{padding:18px}.title{font-size:34px}.table-wrap{overflow:visible}table{min-width:0}thead{display:none}tr{display:block;border-bottom:1px solid var(--line);padding:10px 0}tr:last-child{border-bottom:0}td{display:flex;justify-content:space-between;gap:16px;border-bottom:0;padding:8px 14px;white-space:normal;text-align:right}td::before{content:attr(data-label);flex:0 0 auto;color:var(--muted);font-size:12px;font-weight:800;letter-spacing:.08em;text-align:left;text-transform:uppercase}td:first-child{display:block;text-align:left}td:first-child::before{display:block;margin-bottom:5px}}</style></head><body><main class="wrap">`)
	fmt.Fprintf(&b, `<section class="hero"><div><p class="eyebrow">Customer billing</p><h1 class="title">%s</h1><p class="sub">按当前 API Key 的月度账期汇总消费、请求量和 Token 使用量。</p></div><div class="badge">Key <span>%s</span></div></section>`, html.EscapeString(displayName), html.EscapeString(maskedKey))
	b.WriteString(`<section class="panel"><div class="summary">`)
	fmt.Fprintf(&b, `<article class="metric hero-metric"><div class="label">当前账期</div><div class="value">%s</div><div class="hint">%s 至 %s</div><div class="bar"><div class="fill" style="width:%s"></div></div></article>`, html.EscapeString(formatBillingMoney(used)), html.EscapeString(formatBillingDate(billingString(current, "start"))), html.EscapeString(formatBillingDate(billingString(current, "end"))), html.EscapeString(formatBillingPercent(used, limit)))
	fmt.Fprintf(&b, `<article class="metric"><div class="label">剩余额度</div><div class="value">%s</div><div class="hint">月限额 %s</div></article>`, html.EscapeString(formatBillingLimit(remaining, limit)), html.EscapeString(formatBillingLimit(limit, limit)))
	fmt.Fprintf(&b, `<article class="metric"><div class="label">请求</div><div class="value">%s</div><div class="hint">%s 成功 / %s 失败</div></article>`, html.EscapeString(formatBillingInt(requests)), html.EscapeString(formatBillingInt(success)), html.EscapeString(formatBillingInt(failed)))
	fmt.Fprintf(&b, `<article class="metric"><div class="label">Token</div><div class="value">%s</div><div class="hint"><span class="status %s">%s</span></div></article>`, html.EscapeString(formatBillingInt(tokens)), html.EscapeString(statusClass), html.EscapeString(statusText))
	b.WriteString(`</div><div class="section"><div class="section-head"><h2>历史账期</h2>`)
	if strings.TrimSpace(anchor) != "" {
		fmt.Fprintf(&b, `<span class="sub">锚点：%s</span>`, html.EscapeString(formatBillingDate(anchor)))
	}
	b.WriteString(`</div>`)
	if len(history) == 0 {
		b.WriteString(`<div class="empty">暂无历史账期数据。</div>`)
	} else {
		b.WriteString(`<div class="table-wrap"><table><thead><tr><th>账期</th><th>消费</th><th>请求</th><th>成功</th><th>失败</th><th>Token</th><th>状态</th></tr></thead><tbody>`)
		for _, cycle := range history {
			cycleStatus := "正常"
			cycleClass := "ok"
			if exceeded, _ := cycle["exceeded"].(bool); exceeded {
				cycleStatus = "已超限"
				cycleClass = "danger"
			}
			fmt.Fprintf(&b, `<tr><td data-label="账期">%s - %s</td><td data-label="消费">%s</td><td data-label="请求">%s</td><td data-label="成功">%s</td><td data-label="失败">%s</td><td data-label="Token">%s</td><td data-label="状态"><span class="status %s">%s</span></td></tr>`,
				html.EscapeString(formatBillingDate(billingString(cycle, "start"))),
				html.EscapeString(formatBillingDate(billingString(cycle, "end"))),
				html.EscapeString(formatBillingMoney(billingFloat(cycle, "total_cost"))),
				html.EscapeString(formatBillingInt(billingInt(cycle, "request_count"))),
				html.EscapeString(formatBillingInt(billingInt(cycle, "success_count"))),
				html.EscapeString(formatBillingInt(billingInt(cycle, "failed_count"))),
				html.EscapeString(formatBillingInt(billingInt(cycle, "total_tokens"))),
				html.EscapeString(cycleClass),
				html.EscapeString(cycleStatus),
			)
		}
		b.WriteString(`</tbody></table></div>`)
	}
	b.WriteString(`<p class="foot">出于安全原因，此页面不展示完整 API Key，且响应不会被缓存。</p></div></section></main></body></html>`)
	return b.String()
}

func renderAPIKeyBillingAlpineHTML(c *gin.Context, payload gin.H) {
	apiKey, _ := payload["api_key"].(string)
	name, _ := payload["name"].(string)
	maskedKey := maskAPIKey(apiKey)
	if maskedKey == "" {
		maskedKey = "sk-***"
	}
	displayName := strings.TrimSpace(name)
	if displayName == "" {
		displayName = "API Key"
	}

	// Only expose minimal fields to the browser; never echo the full key.
	publicPayload := gin.H{
		"name":                   displayName,
		"masked_key":             maskedKey,
		"monthly_spending_limit": payload["monthly_spending_limit"],
		"billing_cycle_anchor":   payload["billing_cycle_anchor"],
		"current_cycle":          payload["current_cycle"],
		"history":                payload["history"],
		"model_breakdown":        payload["model_breakdown"],
	}

	jsonBytes, err := json.Marshal(publicPayload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode billing data"})
		return
	}

	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, buildAlpineBillingHTML(jsonBytes))
}

func buildAlpineBillingHTML(data []byte) string {
	// JSON inside <script type="application/json"> is raw text — HTML entities are NOT decoded by browsers.
	// Only escape < to \u003c to prevent </script> injection; JSON.parse will decode \uXXXX escapes correctly.
	safeJSON := strings.ReplaceAll(string(data), "<", "\u003c")
	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="zh-CN" x-data="billingPage" x-init="init()" x-cloak>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>API Key 账单</title>
<style>
[x-cloak]{display:none!important}
:root{--bg:#0f172a;--panel:#1e293b;--panel2:#334155;--text:#f1f5f9;--muted:#94a3b8;--line:#334155;--accent:#f59e0b;--accent2:#fb923c;--danger:#ef4444;--ok:#22c55e;--shadow:0 20px 60px rgba(0,0,0,.4)}
*{box-sizing:border-box;margin:0;padding:0}
body{min-height:100vh;background:linear-gradient(135deg,#0f172a 0%,#1e1b4b 50%,#0f172a 100%);color:var(--text);font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;-webkit-font-smoothing:antialiased}
body::before{content:"";position:fixed;inset:0;background:radial-gradient(circle at 80% 10%,rgba(245,158,11,.12),transparent 40%),radial-gradient(circle at 10% 90%,rgba(251,146,60,.08),transparent 40%);pointer-events:none;z-index:0}
.wrap{position:relative;z-index:1;max-width:1100px;margin:0 auto;padding:40px 20px}
.hero{display:flex;justify-content:space-between;align-items:flex-end;gap:20px;margin-bottom:28px;flex-wrap:wrap}
.hero-left .eyebrow{font-size:11px;font-weight:800;letter-spacing:.2em;text-transform:uppercase;color:var(--accent);margin-bottom:6px}
.hero-left .title{font-size:clamp(28px,5vw,44px);font-weight:900;letter-spacing:-.03em;line-height:1.05}
.hero-left .sub{margin-top:8px;color:var(--muted);font-size:14px}
.badge{display:inline-flex;align-items:center;gap:8px;border:1px solid var(--line);background:rgba(30,41,59,.8);border-radius:12px;padding:10px 16px;font-size:13px;backdrop-filter:blur(10px)}
.badge .dot{width:8px;height:8px;border-radius:50%;background:var(--ok)}
.badge .key{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--accent);font-weight:700}
.cards{display:grid;grid-template-columns:1.4fr 1fr 1fr 1fr;gap:16px;margin-bottom:28px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:20px;padding:24px;position:relative;overflow:hidden;backdrop-filter:blur(10px)}
.card.hero-card{background:linear-gradient(135deg,rgba(245,158,11,.15),rgba(251,146,60,.04)),var(--panel);border-color:rgba(245,158,11,.25)}
.card .label{font-size:11px;font-weight:800;letter-spacing:.12em;text-transform:uppercase;color:var(--muted)}
.card .value{font-size:clamp(24px,3.5vw,34px);font-weight:900;letter-spacing:-.02em;margin-top:10px}
.card .hint{margin-top:6px;font-size:12px;color:var(--muted)}
.bar{height:10px;border-radius:999px;background:var(--panel2);overflow:hidden;margin-top:14px}
.bar .fill{height:100%;width:0;border-radius:inherit;background:linear-gradient(90deg,var(--accent),var(--accent2));transition:width .8s cubic-bezier(.4,0,.2,1)}
.pill{display:inline-flex;align-items:center;gap:6px;border-radius:999px;padding:4px 10px;font-size:11px;font-weight:700}
.pill.ok{background:rgba(34,197,94,.15);color:var(--ok)}
.pill.danger{background:rgba(239,68,68,.15);color:var(--danger)}
.section-head{display:flex;justify-content:space-between;align-items:center;margin-bottom:16px}
.section-head h2{font-size:18px;font-weight:800}
.section-head .meta{font-size:12px;color:var(--muted)}
.section-space{margin-top:28px}
.table-wrap{background:var(--panel);border:1px solid var(--line);border-radius:20px;overflow-x:auto;overflow-y:hidden;backdrop-filter:blur(10px)}
table{width:100%;border-collapse:collapse}
th{padding:14px 16px;text-align:left;font-size:11px;text-transform:uppercase;letter-spacing:.1em;color:var(--muted);border-bottom:1px solid var(--line)}
td{padding:14px 16px;font-size:14px;border-bottom:1px solid var(--line)}
tr:last-child td{border-bottom:0}
tr:hover{background:rgba(245,158,11,.04)}
.model-breakdown-table{min-width:920px;table-layout:fixed}
.model-breakdown-table th:nth-child(1){width:22%}
.model-breakdown-table th:nth-child(2),.model-breakdown-table th:nth-child(3),.model-breakdown-table th:nth-child(4){width:15%}
.model-breakdown-table th:nth-child(5){width:22%}
.model-breakdown-table th:nth-child(6){width:11%}
.model-breakdown-table td{vertical-align:top}
.model-cell{min-width:190px}
.model-name{font-weight:800}
.model-price{color:var(--muted);font-size:12px;margin-top:4px}
.share{min-width:120px}
.share-label{display:flex;justify-content:space-between;gap:10px;color:var(--muted);font-size:12px}
.share-track{height:6px;border-radius:999px;background:var(--panel2);overflow:hidden;margin-top:6px}
.share-fill{height:100%;border-radius:inherit;background:linear-gradient(90deg,var(--accent),var(--accent2))}
.money-sub{font-size:12px;color:var(--muted)}
.token-mix{display:flex;flex-direction:column;gap:8px;min-width:180px}
.token-row{display:grid;grid-template-columns:34px 1fr;gap:8px;align-items:center}
.token-row-label{color:var(--muted);font-size:12px;font-weight:800}
.token-row-main{min-width:0}
.token-row-text{display:flex;justify-content:space-between;gap:8px;color:var(--muted);font-size:12px}
.token-row-text strong{color:var(--text);font-size:12px}
.discount.good{color:var(--ok);font-weight:800}
.discount.neutral{color:var(--muted);font-weight:800}
.discount.bad{color:var(--danger);font-weight:800}
.empty{padding:32px;text-align:center;color:var(--muted);font-size:14px}
.foot{margin-top:20px;text-align:center;color:var(--muted);font-size:12px}
.mobile-cards{display:none}
.model-mobile-cards{display:none}
@media(max-width:768px){
.hero{flex-direction:column;align-items:flex-start}
.cards{grid-template-columns:1fr 1fr;gap:12px}
.card{padding:18px}
.table-wrap{display:none}
.mobile-cards,.model-mobile-cards{display:flex;flex-direction:column;gap:12px}
.mobile-card{background:var(--panel);border:1px solid var(--line);border-radius:16px;padding:16px}
.mobile-card .mc-head{display:flex;justify-content:space-between;align-items:center;margin-bottom:12px}
.mobile-card .mc-date{font-size:13px;font-weight:700;color:var(--accent)}
.mobile-card .mc-grid{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.mobile-card .mc-item .l{font-size:10px;color:var(--muted);text-transform:uppercase;letter-spacing:.08em}
.mobile-card .mc-item .v{font-size:15px;font-weight:700;margin-top:2px}
}
@media(max-width:480px){.cards{grid-template-columns:1fr}}
</style>
<script type="application/json" id="billing-data">` + safeJSON + `</script>
<script>
function billingPage(){
  return {
    data:{},
    expanded:-1,
    init(){
      try{
        var el=document.getElementById('billing-data');
        this.data=JSON.parse(el.textContent);
      }catch(e){this.data={};}
    },
    fmtMoney(v){
      v=Number(v)||0;
      return '$'+v.toFixed(2);
    },
    fmtLimit(v){
      v=Number(v)||0;
      if(v<=0) return '不限';
      return '$'+v.toFixed(2);
    },
    fmtInt(v){
      v=Number(v)||0;
      return v.toLocaleString('en-US');
    },
    fmtToken(v){
      v=Number(v)||0;
      return (v/1000000).toFixed(2)+'M';
    },
    tokenPartShare(m, key){
      if(!m) return 0;
      var total=Number(m.total_tokens)||0;
      if(total<=0) return 0;
      var value=Number(m[key])||0;
      if(key==='output_tokens') value+=(Number(m.reasoning_tokens)||0);
      return value/total;
    },
    fmtDate(s){
      if(!s) return '--';
      try{var d=new Date(s);return d.getFullYear()+'-'+String(d.getMonth()+1).padStart(2,'0')+'-'+String(d.getDate()).padStart(2,'0');}
      catch(e){return s;}
    },
    fmtPct(used,limit){
      if(!limit||limit<=0) return 0;
      var p=(used/limit)*100;
      if(p<0)p=0;if(p>100)p=100;
      return p.toFixed(1);
    },
    fmtShare(v){
      v=Number(v)||0;
      if(v<0)v=0;
      return (v*100).toFixed(1)+'%';
    },
    shareWidth(v){
      v=Number(v)||0;
      if(v<0)v=0;if(v>1)v=1;
      return (v*100).toFixed(1)+'%';
    },
    fmtPrice(m){
      if(!m||!m.has_price) return '未配置价格';
      if(m.price_mode==='call') return this.fmtMoney(m.price_per_call)+' / 次';
      return 'In '+this.fmtMoney(m.input_price_per_million)+' / Out '+this.fmtMoney(m.output_price_per_million)+' / M';
    },
    fmtDiscount(m){
      if(!m||!m.has_price||!m.estimated_list_cost) return '无价格';
      var v=Number(m.discount_rate)||0;
      if(Math.abs(v)<0.0005) return '无折扣';
      if(v>0) return (v*100).toFixed(1)+'% off';
      return '+'+(Math.abs(v)*100).toFixed(1)+'%';
    },
    discountClass(m){
      if(!m||!m.has_price||!m.estimated_list_cost) return 'neutral';
      var v=Number(m.discount_rate)||0;
      if(Math.abs(v)<0.0005) return 'neutral';
      return v>0?'good':'bad';
    },
    get cur(){return this.data.current_cycle||{};},
    get hist(){return this.data.history||[];},
    get models(){return this.data.model_breakdown||this.cur.model_breakdown||[];},
    get limit(){return Number(this.data.monthly_spending_limit)||0;},
    get used(){return Number(this.cur.total_cost)||0;},
    get remaining(){return Number(this.cur.remaining)||0;},
    get pct(){return this.fmtPct(this.used,this.limit);},
    get exceeded(){return !!this.cur.exceeded;}
  }
}
</script>
<script defer src="https://cdn.jsdelivr.net/npm/alpinejs@3.14.8/dist/cdn.min.js"></script>
</head>
<body>
<div class="wrap">
  <section class="hero">
    <div class="hero-left">
      <p class="eyebrow">Billing Dashboard</p>
      <h1 class="title" x-text="data.name||'API Key'"></h1>
      <p class="sub">按当前 API Key 的月度账期汇总消费、请求量和 Token 使用量。</p>
    </div>
    <div class="badge">
      <span class="dot"></span>
      <span>Key</span>
      <span class="key" x-text="data.masked_key||'sk-***'"></span>
    </div>
  </section>

  <div class="cards">
    <div class="card hero-card">
      <div class="label">当前账期消费</div>
      <div class="value" x-text="fmtMoney(used)"></div>
      <div class="hint" x-text="fmtDate(cur.start)+' 至 '+fmtDate(cur.end)"></div>
      <div class="bar"><div class="fill" :style="'width:'+pct+'%'"></div></div>
    </div>
    <div class="card">
      <div class="label">剩余额度</div>
      <div class="value" x-text="fmtLimit(remaining)"></div>
      <div class="hint">月限额 <span x-text="fmtLimit(limit)"></span></div>
    </div>
    <div class="card">
      <div class="label">请求量</div>
      <div class="value" x-text="fmtInt(cur.request_count)"></div>
      <div class="hint"><span x-text="fmtInt(cur.success_count)"></span> 成功 / <span x-text="fmtInt(cur.failed_count)"></span> 失败</div>
    </div>
    <div class="card">
      <div class="label">Token</div>
      <div class="value" x-text="fmtToken(cur.total_tokens)"></div>
      <div class="hint">
        <span class="pill" :class="exceeded?'danger':'ok'" x-text="exceeded?'已超限':'正常'"></span>
      </div>
    </div>
  </div>

  <div class="section-head">
    <h2>模型明细</h2>
    <span class="meta">次数 / Token / 金额占比</span>
  </div>

  <div class="table-wrap" x-show="models.length>0">
    <table class="model-breakdown-table">
      <thead>
        <tr><th>模型</th><th>次数</th><th>Token</th><th>金额</th><th>Token结构</th><th>折扣</th></tr>
      </thead>
      <tbody>
        <template x-for="m in models" :key="m.model">
          <tr>
            <td class="model-cell">
              <div class="model-name" x-text="m.model"></div>
              <div class="model-price" x-text="fmtPrice(m)"></div>
            </td>
            <td class="share">
              <div class="share-label"><span x-text="fmtInt(m.request_count)+' 次'"></span><span x-text="fmtShare(m.request_share)"></span></div>
              <div class="share-track"><div class="share-fill" :style="'width:'+shareWidth(m.request_share)"></div></div>
            </td>
            <td class="share">
              <div class="share-label"><span x-text="fmtToken(m.total_tokens)"></span><span x-text="fmtShare(m.token_share)"></span></div>
              <div class="share-track"><div class="share-fill" :style="'width:'+shareWidth(m.token_share)"></div></div>
            </td>
            <td class="share">
              <div class="share-label"><span x-text="fmtMoney(m.total_cost)"></span><span x-text="fmtShare(m.cost_share)"></span></div>
              <div class="share-track"><div class="share-fill" :style="'width:'+shareWidth(m.cost_share)"></div></div>
              <div class="money-sub" style="margin-top:6px" x-text="m.has_price?'原价 '+fmtMoney(m.estimated_list_cost):'未配置价格'"></div>
            </td>
            <td>
              <div class="token-mix">
                <div class="token-row">
                  <div class="token-row-label">输入</div>
                  <div class="token-row-main">
                    <div class="token-row-text"><span x-text="fmtToken(m.input_tokens)"></span><strong x-text="fmtShare(tokenPartShare(m,'input_tokens'))"></strong></div>
                    <div class="share-track"><div class="share-fill" :style="'width:'+shareWidth(tokenPartShare(m,'input_tokens'))"></div></div>
                  </div>
                </div>
                <div class="token-row">
                  <div class="token-row-label">输出</div>
                  <div class="token-row-main">
                    <div class="token-row-text"><span x-text="fmtToken((Number(m.output_tokens)||0)+(Number(m.reasoning_tokens)||0))"></span><strong x-text="fmtShare(tokenPartShare(m,'output_tokens'))"></strong></div>
                    <div class="share-track"><div class="share-fill" :style="'width:'+shareWidth(tokenPartShare(m,'output_tokens'))"></div></div>
                  </div>
                </div>
                <div class="token-row">
                  <div class="token-row-label">缓存</div>
                  <div class="token-row-main">
                    <div class="token-row-text"><span x-text="fmtToken(m.cached_tokens)"></span><strong x-text="fmtShare(tokenPartShare(m,'cached_tokens'))"></strong></div>
                    <div class="share-track"><div class="share-fill" :style="'width:'+shareWidth(tokenPartShare(m,'cached_tokens'))"></div></div>
                  </div>
                </div>
              </div>
            </td>
            <td><span class="discount" :class="discountClass(m)" x-text="fmtDiscount(m)"></span></td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>

  <div class="model-mobile-cards" x-show="models.length>0">
    <template x-for="m in models" :key="m.model">
      <div class="mobile-card">
        <div class="mc-head">
          <span class="mc-date" x-text="m.model"></span>
          <span class="discount" :class="discountClass(m)" x-text="fmtDiscount(m)"></span>
        </div>
        <div class="model-price" x-text="fmtPrice(m)"></div>
        <div class="mc-grid" style="margin-top:12px">
          <div class="mc-item"><div class="l">次数</div><div class="v" x-text="fmtInt(m.request_count)+' / '+fmtShare(m.request_share)"></div></div>
          <div class="mc-item"><div class="l">Token</div><div class="v" x-text="fmtToken(m.total_tokens)+' / '+fmtShare(m.token_share)"></div></div>
          <div class="mc-item"><div class="l">金额</div><div class="v" x-text="fmtMoney(m.total_cost)+' / '+fmtShare(m.cost_share)"></div></div>
          <div class="mc-item"><div class="l">原价</div><div class="v" x-text="m.has_price?fmtMoney(m.estimated_list_cost):'--'"></div></div>
        </div>
        <div class="token-mix" style="margin-top:14px">
          <div class="token-row">
            <div class="token-row-label">输入</div>
            <div class="token-row-main">
              <div class="token-row-text"><span x-text="fmtToken(m.input_tokens)"></span><strong x-text="fmtShare(tokenPartShare(m,'input_tokens'))"></strong></div>
              <div class="share-track"><div class="share-fill" :style="'width:'+shareWidth(tokenPartShare(m,'input_tokens'))"></div></div>
            </div>
          </div>
          <div class="token-row">
            <div class="token-row-label">输出</div>
            <div class="token-row-main">
              <div class="token-row-text"><span x-text="fmtToken((Number(m.output_tokens)||0)+(Number(m.reasoning_tokens)||0))"></span><strong x-text="fmtShare(tokenPartShare(m,'output_tokens'))"></strong></div>
              <div class="share-track"><div class="share-fill" :style="'width:'+shareWidth(tokenPartShare(m,'output_tokens'))"></div></div>
            </div>
          </div>
          <div class="token-row">
            <div class="token-row-label">缓存</div>
            <div class="token-row-main">
              <div class="token-row-text"><span x-text="fmtToken(m.cached_tokens)"></span><strong x-text="fmtShare(tokenPartShare(m,'cached_tokens'))"></strong></div>
              <div class="share-track"><div class="share-fill" :style="'width:'+shareWidth(tokenPartShare(m,'cached_tokens'))"></div></div>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>

  <div class="empty" x-show="models.length===0">暂无模型明细。</div>

  <div class="section-head">
    <h2>历史账期</h2>
    <span class="meta" x-show="data.billing_cycle_anchor" x-text="'锚点：'+fmtDate(data.billing_cycle_anchor)"></span>
  </div>

  <div class="table-wrap" x-show="hist.length>0">
    <table>
      <thead>
        <tr><th>账期</th><th>消费</th><th>请求</th><th>成功</th><th>失败</th><th>Token</th><th>状态</th></tr>
      </thead>
      <tbody>
        <template x-for="(c,i) in hist" :key="i">
          <tr>
            <td x-text="fmtDate(c.start)+' - '+fmtDate(c.end)"></td>
            <td x-text="fmtMoney(c.total_cost)"></td>
            <td x-text="fmtInt(c.request_count)"></td>
            <td x-text="fmtInt(c.success_count)"></td>
            <td x-text="fmtInt(c.failed_count)"></td>
            <td x-text="fmtToken(c.total_tokens)"></td>
            <td><span class="pill" :class="c.exceeded?'danger':'ok'" x-text="c.exceeded?'已超限':'正常'"></span></td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>

  <div class="mobile-cards" x-show="hist.length>0">
    <template x-for="(c,i) in hist" :key="i">
      <div class="mobile-card">
        <div class="mc-head">
          <span class="mc-date" x-text="fmtDate(c.start)+' - '+fmtDate(c.end)"></span>
          <span class="pill" :class="c.exceeded?'danger':'ok'" x-text="c.exceeded?'已超限':'正常'"></span>
        </div>
        <div class="mc-grid">
          <div class="mc-item"><div class="l">消费</div><div class="v" x-text="fmtMoney(c.total_cost)"></div></div>
          <div class="mc-item"><div class="l">请求</div><div class="v" x-text="fmtInt(c.request_count)"></div></div>
          <div class="mc-item"><div class="l">成功</div><div class="v" x-text="fmtInt(c.success_count)"></div></div>
          <div class="mc-item"><div class="l">失败</div><div class="v" x-text="fmtInt(c.failed_count)"></div></div>
          <div class="mc-item"><div class="l">Token</div><div class="v" x-text="fmtToken(c.total_tokens)"></div></div>
        </div>
      </div>
    </template>
  </div>

  <div class="empty" x-show="hist.length===0">暂无历史账期数据。</div>

  <p class="foot">出于安全原因，此页面不展示完整 API Key，且响应不会被缓存。</p>
</div>
</body>
</html>`)
	return b.String()
}

func billingString(payload gin.H, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func billingFloat(payload gin.H, key string) float64 {
	switch value := payload[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		parsed, _ := value.Float64()
		return parsed
	default:
		return 0
	}
}

func billingInt(payload gin.H, key string) int64 {
	switch value := payload[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

func formatBillingMoney(value float64) string {
	return fmt.Sprintf("$%.4f", value)
}

func formatBillingLimit(value, limit float64) string {
	if limit <= 0 {
		return "不限"
	}
	return formatBillingMoney(value)
}

func formatBillingInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

func formatBillingPercent(used, limit float64) string {
	if limit <= 0 {
		return "0%"
	}
	percent := used / limit * 100
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("%.2f%%", percent)
}

func formatBillingDate(value string) string {
	if value == "" {
		return "--"
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.Local().Format("2006-01-02 15:04")
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
	payload := clearUsageLogData(db, req.ClearBodyContent, req.ClearDetailContent, req.ClearRequestRecords)
	if req.ClearRequestRecords {
		h.invalidateUsageAggregateCache()
	}
	c.JSON(http.StatusOK, payload)
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
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	payload := emptyAuthFileTrendPayload(authIndex, days, hours)
	if authIndex == "" {
		c.JSON(http.StatusOK, payload)
		return
	}
	db, ok := h.usageDB()
	if !ok {
		c.JSON(http.StatusOK, payload)
		return
	}
	defer func() { _ = db.Close() }()
	if !dbTableExists(db, "request_logs") {
		c.JSON(http.StatusOK, payload)
		return
	}

	filters := usageFilters{Days: days, AuthIndexes: []string{authIndex}}
	totals := queryUsageTotals(db, filters)
	payload["request_total"] = totals.Total
	payload["daily_usage"] = queryAuthFileDailyUsage(db, filters)

	cycleStart := time.Now().UTC().AddDate(0, 0, -days)
	payload["cycle_start"] = cycleStart.Format(time.RFC3339)
	payload["cycle_request_total"] = totals.Total

	hourlyFilters := usageFilters{
		AuthIndexes: []string{authIndex},
		Start:       time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339),
	}
	payload["hourly_usage"] = queryAuthFileHourlyUsage(db, hourlyFilters)
	c.JSON(http.StatusOK, payload)
}

func emptyAuthFileTrendPayload(authIndex string, days, hours int) gin.H {
	return gin.H{
		"auth_index":          authIndex,
		"days":                days,
		"hours":               hours,
		"request_total":       0,
		"cycle_request_total": 0,
		"cycle_start":         "",
		"daily_usage":         []gin.H{},
		"hourly_usage":        []gin.H{},
		"quota_series":        []gin.H{},
	}
}

func queryAuthFileDailyUsage(db *sql.DB, filters usageFilters) []gin.H {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.Query("SELECT date("+usageColumnExpr(cols, "timestamp", "datetime('now')")+") as d, count(*) FROM request_logs WHERE "+whereSQL+" GROUP BY d ORDER BY d", args...)
	if err != nil || rows == nil {
		return []gin.H{}
	}
	defer func() { _ = rows.Close() }()
	out := []gin.H{}
	for rows.Next() {
		var date string
		var requests int64
		if rows.Scan(&date, &requests) == nil {
			out = append(out, gin.H{"date": date, "requests": requests})
		}
	}
	return out
}

func queryAuthFileHourlyUsage(db *sql.DB, filters usageFilters) []gin.H {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.Query("SELECT strftime('%Y-%m-%d %H:00', "+usageColumnExpr(cols, "timestamp", "datetime('now')")+"), count(*) FROM request_logs WHERE "+whereSQL+" GROUP BY 1 ORDER BY 1", args...)
	if err != nil || rows == nil {
		return []gin.H{}
	}
	defer func() { _ = rows.Close() }()
	out := []gin.H{}
	for rows.Next() {
		var hour string
		var requests int64
		if rows.Scan(&hour, &requests) == nil {
			out = append(out, gin.H{"hour": hour, "requests": requests})
		}
	}
	return out
}

func (h *Handler) ReconcileQuota(c *gin.Context) {
	var req struct {
		AuthIndex string `json:"authIndex"`
		AuthID    string `json:"auth_id"`
		ID        string `json:"id"`
	}
	_ = c.ShouldBindJSON(&req)
	authID := strings.TrimSpace(req.AuthIndex)
	if authID == "" {
		authID = strings.TrimSpace(req.AuthID)
	}
	if authID == "" {
		authID = strings.TrimSpace(req.ID)
	}
	if authID == "" {
		authID = strings.TrimSpace(c.Query("authIndex"))
	}
	if authID == "" {
		authID = strings.TrimSpace(c.Query("auth_id"))
	}
	if authID == "" {
		authID = strings.TrimSpace(c.Query("id"))
	}
	if authID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "auth id is required"})
		return
	}
	changed := false
	if h.authManager != nil {
		if auth := h.authByIndex(authID); auth != nil {
			authID = auth.ID
		}
		var err error
		changed, err = h.authManager.ReconcileQuota(c.Request.Context(), authID)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"status": "error", "error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "changed": changed})
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
	// request_logs timestamps are UTC RFC3339/RFC3339Nano. Keep functions on
	// the bounds so SQLite can use idx_logs_timestamp; the next-second upper
	// bound preserves the previous datetime(...) comparison's second precision.
	if f.Days > 0 && cols["timestamp"] {
		parts = append(parts, "timestamp >= strftime('%Y-%m-%dT%H:%M:%S', 'now', ?)")
		args = append(args, "-"+strconv.Itoa(f.Days)+" days")
	}
	if f.Start != "" && cols["timestamp"] {
		parts = append(parts, "timestamp >= strftime('%Y-%m-%dT%H:%M:%S', ?)")
		args = append(args, f.Start)
	}
	if f.End != "" && cols["timestamp"] {
		parts = append(parts, "timestamp < strftime('%Y-%m-%dT%H:%M:%S', ?, '+1 second')")
		args = append(args, f.End)
	} else if (f.Days > 0 || f.Start != "") && cols["timestamp"] {
		parts = append(parts, "timestamp < strftime('%Y-%m-%dT%H:%M:%S', 'now', '+1 second')")
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

func buildUsageChartPayloadContext(ctx context.Context, db *sql.DB, filters usageFilters, public bool) (gin.H, error) {
	payload := emptyUsageChartPayload()
	daily, err := queryUsageDailySeriesResultContext(ctx, db, filters)
	if err != nil {
		return nil, err
	}
	models, err := queryUsageModelDistributionResultContext(ctx, db, filters)
	if err != nil {
		return nil, err
	}
	hourlyTokens, err := queryUsageHourlyTokensResultContext(ctx, db, filters)
	if err != nil {
		return nil, err
	}
	hourlyModels, err := queryUsageHourlyModelsResultContext(ctx, db, filters)
	if err != nil {
		return nil, err
	}
	payload["daily_series"] = daily
	payload["model_distribution"] = models
	payload["hourly_tokens"] = hourlyTokens
	payload["hourly_models"] = hourlyModels
	if !public {
		apiKeys, errAPIKeys := queryUsageAPIKeyDistributionResultContext(ctx, db, filters)
		if errAPIKeys != nil {
			return nil, errAPIKeys
		}
		payload["apikey_distribution"] = apiKeys
	}
	totals, err := queryUsageTotalsResultContext(ctx, db, filters)
	if err != nil {
		return nil, err
	}
	successRate := float64(0)
	if totals.Total > 0 {
		successRate = float64(totals.Success) / float64(totals.Total) * 100
	}
	payload["stats"] = gin.H{"total": totals.Total, "success_rate": successRate, "total_tokens": totals.TotalTokens, "total_cost": totals.TotalCost}
	return payload, nil
}

func queryUsageTotals(db *sql.DB, filters usageFilters) usageTotals {
	return queryUsageTotalsContext(context.Background(), db, filters)
}

func queryUsageTotalsContext(ctx context.Context, db *sql.DB, filters usageFilters) usageTotals {
	out, _ := queryUsageTotalsResultContext(ctx, db, filters)
	return out
}

func queryUsageTotalsResultContext(ctx context.Context, db *sql.DB, filters usageFilters) (usageTotals, error) {
	if !dbTableExists(db, "request_logs") {
		return usageTotals{}, nil
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	row := db.QueryRowContext(ctx, "SELECT count(*), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"=0 then 1 else 0 end),0), coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"!=0 then 1 else 0 end),0), coalesce(sum("+usageColumnExpr(cols, "input_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "output_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "reasoning_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "cached_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "cost", "0")+"),0) FROM request_logs WHERE "+whereSQL, args...)
	var out usageTotals
	if err := row.Scan(&out.Total, &out.Success, &out.Failed, &out.InputTokens, &out.OutputTokens, &out.ReasoningTokens, &out.CachedTokens, &out.TotalTokens, &out.TotalCost); err != nil {
		return usageTotals{}, err
	}
	return out, nil
}

func queryUsageDailySeries(db *sql.DB, filters usageFilters) []gin.H {
	return queryUsageDailySeriesContext(context.Background(), db, filters)
}

func queryUsageDailySeriesContext(ctx context.Context, db *sql.DB, filters usageFilters) []gin.H {
	out, _ := queryUsageDailySeriesResultContext(ctx, db, filters)
	return out
}

func queryUsageDailySeriesResultContext(ctx context.Context, db *sql.DB, filters usageFilters) ([]gin.H, error) {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}, nil
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.QueryContext(ctx, "SELECT date("+usageColumnExpr(cols, "timestamp", "datetime('now')")+") as d, count(*) as cnt, coalesce(sum(case when "+usageColumnExpr(cols, "failed", "0")+"!=0 then 1 else 0 end),0), coalesce(sum("+usageColumnExpr(cols, "input_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "output_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "reasoning_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "cached_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "cost", "0")+"),0) FROM request_logs WHERE "+whereSQL+" GROUP BY d ORDER BY d", args...)
	if err != nil || rows == nil {
		return nil, err
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
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func queryUsageModelDistribution(db *sql.DB, filters usageFilters) []gin.H {
	return queryUsageModelDistributionContext(context.Background(), db, filters)
}

func queryUsageModelDistributionContext(ctx context.Context, db *sql.DB, filters usageFilters) []gin.H {
	out, _ := queryUsageModelDistributionResultContext(ctx, db, filters)
	return out
}

func queryUsageModelDistributionResultContext(ctx context.Context, db *sql.DB, filters usageFilters) ([]gin.H, error) {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}, nil
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.QueryContext(ctx, "SELECT "+usageColumnExpr(cols, "model", "''")+", count(*) as cnt, coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0) FROM request_logs WHERE "+whereSQL+" GROUP BY "+usageColumnExpr(cols, "model", "''")+" ORDER BY cnt DESC LIMIT 20", args...)
	if err != nil || rows == nil {
		return nil, err
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
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func queryUsageHourlyTokens(db *sql.DB, filters usageFilters) []gin.H {
	return queryUsageHourlyTokensContext(context.Background(), db, filters)
}

func queryUsageHourlyTokensContext(ctx context.Context, db *sql.DB, filters usageFilters) []gin.H {
	out, _ := queryUsageHourlyTokensResultContext(ctx, db, filters)
	return out
}

func queryUsageHourlyTokensResultContext(ctx context.Context, db *sql.DB, filters usageFilters) ([]gin.H, error) {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}, nil
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.QueryContext(ctx, "SELECT strftime('%Y-%m-%d %H:00', "+usageColumnExpr(cols, "timestamp", "datetime('now')")+"), coalesce(sum("+usageColumnExpr(cols, "input_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "output_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "reasoning_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "cached_tokens", "0")+"),0), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0) FROM request_logs WHERE "+whereSQL+" GROUP BY 1 ORDER BY 1", args...)
	if err != nil || rows == nil {
		return nil, err
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
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func queryUsageHourlyModels(db *sql.DB, filters usageFilters) []gin.H {
	return queryUsageHourlyModelsContext(context.Background(), db, filters)
}

func queryUsageHourlyModelsContext(ctx context.Context, db *sql.DB, filters usageFilters) []gin.H {
	out, _ := queryUsageHourlyModelsResultContext(ctx, db, filters)
	return out
}

func queryUsageHourlyModelsResultContext(ctx context.Context, db *sql.DB, filters usageFilters) ([]gin.H, error) {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}, nil
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.QueryContext(ctx, "SELECT strftime('%Y-%m-%d %H:00', "+usageColumnExpr(cols, "timestamp", "datetime('now')")+"), "+usageColumnExpr(cols, "model", "''")+", count(*) FROM request_logs WHERE "+whereSQL+" GROUP BY 1, 2 ORDER BY 1, 3 DESC", args...)
	if err != nil || rows == nil {
		return nil, err
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
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func queryUsageHourlyThroughput(db *sql.DB, filters usageFilters) []gin.H {
	return queryUsageHourlyThroughputContext(context.Background(), db, filters)
}

func queryUsageHourlyThroughputContext(ctx context.Context, db *sql.DB, filters usageFilters) []gin.H {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	rows, err := db.QueryContext(ctx, "SELECT strftime('%Y-%m-%d %H:00', "+usageColumnExpr(cols, "timestamp", "datetime('now')")+"), count(*), coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0) FROM request_logs WHERE "+whereSQL+" GROUP BY 1 ORDER BY 1", args...)
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
	return queryUsageAPIKeyDistributionContext(context.Background(), db, filters)
}

func queryUsageAPIKeyDistributionContext(ctx context.Context, db *sql.DB, filters usageFilters) []gin.H {
	out, _ := queryUsageAPIKeyDistributionResultContext(ctx, db, filters)
	return out
}

func queryUsageAPIKeyDistributionResultContext(ctx context.Context, db *sql.DB, filters usageFilters) ([]gin.H, error) {
	if !dbTableExists(db, "request_logs") {
		return []gin.H{}, nil
	}
	cols := requestLogColumns(db)
	whereSQL, args := filters.whereClause(db)
	joinSQL, keyExpr, nameExpr := usageAPIKeyNameLookup(db, cols)
	rows, err := db.QueryContext(ctx, "SELECT "+keyExpr+", "+nameExpr+", count(*) as cnt, coalesce(sum("+usageColumnExpr(cols, "total_tokens", "0")+"),0) FROM request_logs"+joinSQL+" WHERE "+whereSQL+" GROUP BY "+keyExpr+", "+nameExpr+" ORDER BY cnt DESC LIMIT 20", args...)
	if err != nil || rows == nil {
		return nil, err
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
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
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

var (
	usageLogFiltersMu    sync.RWMutex
	usageLogFiltersCache = make(map[bool]usageLogFiltersCacheEntry)
)

type usageLogFiltersCacheEntry struct {
	expiresAt time.Time
	filters   gin.H
}

func usageLogFilters(db *sql.DB, public bool) gin.H {
	usageLogFiltersMu.RLock()
	if entry, ok := usageLogFiltersCache[public]; ok && time.Now().Before(entry.expiresAt) {
		usageLogFiltersMu.RUnlock()
		return entry.filters
	}
	usageLogFiltersMu.RUnlock()

	usageLogFiltersMu.Lock()
	defer usageLogFiltersMu.Unlock()
	if entry, ok := usageLogFiltersCache[public]; ok && time.Now().Before(entry.expiresAt) {
		return entry.filters
	}

	cols := requestLogColumns(db)
	models := []string{}
	if cols["model"] {
		models = distinctStringValues(db, "request_logs", "model", "model != ''", nil)
	}
	filters := gin.H{"models": models}
	if public {
		usageLogFiltersCache[public] = usageLogFiltersCacheEntry{
			expiresAt: time.Now().Add(60 * time.Second),
			filters:   filters,
		}
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
	usageLogFiltersCache[public] = usageLogFiltersCacheEntry{
		expiresAt: time.Now().Add(60 * time.Second),
		filters:   filters,
	}
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
	rows, err := db.Query(`select key, name, disabled, daily_limit, total_quota, spending_limit, monthly_spending_limit, billing_cycle_anchor, concurrency_limit, rpm_limit, tpm_limit, allowed_models, allowed_channels, allowed_channel_groups, system_prompt, created_at, ` + selectPermissionProfile + ` from api_keys order by created_at, key`)
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
			&entry.MonthlySpendingLimit,
			&entry.BillingCycleAnchor,
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
		monthly_spending_limit real not null default 0,
		billing_cycle_anchor text not null default '',
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
		key, name, disabled, daily_limit, total_quota, spending_limit, monthly_spending_limit, billing_cycle_anchor, concurrency_limit, rpm_limit, tpm_limit,
		allowed_models, allowed_channels, allowed_channel_groups, system_prompt, created_at, updated_at, permission_profile_id
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
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
			entry.MonthlySpendingLimit,
			strings.TrimSpace(entry.BillingCycleAnchor),
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
