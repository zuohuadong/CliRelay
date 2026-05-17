package management

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
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
	if entries, ok := h.apiKeyEntriesFromDB(); ok {
		c.JSON(http.StatusOK, gin.H{"api-key-entries": entries})
		return
	}
	entries := make([]panelAPIKeyEntry, 0)
	if h != nil && h.cfg != nil {
		entries = entriesFromKeys(h.cfg.APIKeys)
	}
	c.JSON(http.StatusOK, gin.H{"api-key-entries": entries})
}

func (h *Handler) PutAPIKeyEntries(c *gin.Context) {
	entries, ok := readAPIKeyEntries(c)
	if !ok {
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

	entries, dbOK := h.apiKeyEntriesFromDB()
	if !dbOK && h != nil && h.cfg != nil {
		entries = entriesFromKeys(h.cfg.APIKeys)
	}
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
	h.updateConfigAPIKeys(keysFromEntries(entries))
	if dbOK {
		if err := h.replaceAPIKeyEntriesInDB(entries); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save api keys"})
			return
		}
	}
	h.persist(c)
}

func (h *Handler) DeleteAPIKeyEntries(c *gin.Context) {
	entries, dbOK := h.apiKeyEntriesFromDB()
	if !dbOK && h != nil && h.cfg != nil {
		entries = entriesFromKeys(h.cfg.APIKeys)
	}
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
}

func (h *Handler) GetAPIKeyPermissionProfiles(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"api-key-permission-profiles": []gin.H{}})
}

func (h *Handler) PutAPIKeyPermissionProfiles(c *gin.Context) {
	unsupportedPanelWrite(c, "api-key permission profiles are not available in this v7 build")
}

func (h *Handler) GetRoutingConfig(c *gin.Context) {
	strategy := "round-robin"
	if h != nil && h.cfg != nil && strings.TrimSpace(h.cfg.Routing.Strategy) != "" {
		strategy = strings.TrimSpace(h.cfg.Routing.Strategy)
	}
	c.JSON(http.StatusOK, gin.H{
		"strategy":              strategy,
		"include-default-group": true,
		"channel-groups":        []gin.H{},
		"path-routes":           []gin.H{},
	})
}

func (h *Handler) PutRoutingConfig(c *gin.Context) {
	var body struct {
		Strategy      string          `json:"strategy"`
		ChannelGroups json.RawMessage `json:"channel-groups"`
		PathRoutes    json.RawMessage `json:"path-routes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if len(body.ChannelGroups) > 0 && string(body.ChannelGroups) != "null" && string(body.ChannelGroups) != "[]" {
		unsupportedPanelWrite(c, "channel groups are not available in this v7 build")
		return
	}
	if len(body.PathRoutes) > 0 && string(body.PathRoutes) != "null" && string(body.PathRoutes) != "[]" {
		unsupportedPanelWrite(c, "path routes are not available in this v7 build")
		return
	}
	normalized, ok := normalizeRoutingStrategy(body.Strategy)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid routing strategy"})
		return
	}
	h.cfg.Routing.Strategy = normalized
	h.persist(c)
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

func (h *Handler) GetChannelGroups(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"items": []gin.H{}})
}

func (h *Handler) GetCCSwitchImportConfigs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ccswitch-import-configs": []gin.H{}})
}

func (h *Handler) PutCCSwitchImportConfigs(c *gin.Context) {
	unsupportedPanelWrite(c, "ccswitch import presets are not available in this v7 build")
}

func (h *Handler) GetOpenCodeGoKeys(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"opencode-go-api-key": []gin.H{}})
}

func (h *Handler) PutOpenCodeGoKeys(c *gin.Context) {
	unsupportedPanelWrite(c, "opencode-go provider is not available in this v7 build")
}

func (h *Handler) DeleteOpenCodeGoKey(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) GetBedrockKeys(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"bedrock-api-key": []gin.H{}})
}

func (h *Handler) PutBedrockKeys(c *gin.Context) {
	unsupportedPanelWrite(c, "bedrock provider is not available in this v7 build")
}

func (h *Handler) DeleteBedrockKey(c *gin.Context) {
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

func (h *Handler) GetUsageSummary(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"usage": gin.H{
			"total_requests":   0,
			"success_count":    0,
			"failure_count":    0,
			"total_tokens":     0,
			"apis":             gin.H{},
			"requests_by_day":  gin.H{},
			"requests_by_hour": gin.H{},
			"tokens_by_day":    gin.H{},
			"tokens_by_hour":   gin.H{},
		},
	})
}

func (h *Handler) GetUsageChartData(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"daily_series":        []gin.H{},
		"model_distribution":  []gin.H{},
		"hourly_tokens":       []gin.H{},
		"hourly_models":       []gin.H{},
		"apikey_distribution": []gin.H{},
	})
}

func (h *Handler) GetUsageEntityStats(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"source": []gin.H{}, "auth_index": []gin.H{}})
}

func (h *Handler) GetUsageLogs(c *gin.Context) {
	page := positiveQueryInt(c, "page", 1)
	size := positiveQueryInt(c, "size", 50)
	c.JSON(http.StatusOK, gin.H{
		"items": []gin.H{},
		"total": 0,
		"page":  page,
		"size":  size,
		"filters": gin.H{
			"api_keys":      []string{},
			"api_key_names": gin.H{},
			"models":        []string{},
			"channels":      []string{},
		},
		"stats": gin.H{
			"total":        0,
			"success_rate": 0,
			"total_tokens": 0,
			"total_cost":   0,
		},
	})
}

func (h *Handler) DeleteUsageLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"deleted_logs":        0,
		"deleted_contents":    0,
		"cleared_body_rows":   0,
		"cleared_detail_rows": 0,
		"cleared_legacy_rows": 0,
	})
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

func (h *Handler) GetUsageLogContent(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	c.JSON(http.StatusOK, gin.H{
		"id":             id,
		"model":          "",
		"input_content":  "",
		"output_content": "",
		"detail_content": "",
	})
}

func (h *Handler) ReconcileQuota(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
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
	if !apiKeysDBHasColumn(db, "permission_profile_id") {
		if _, err := db.Exec(`alter table api_keys add column permission_profile_id text not null default ''`); err != nil {
			return err
		}
	}
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
