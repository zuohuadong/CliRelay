package management

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

// GetUsageLogs returns paginated, filterable request log entries from SQLite.
// It enriches each log item with resolved api_key_name and channel_name
// from the in-memory config, eliminating the need for multiple frontend API calls.
func (h *Handler) GetUsageLogs(c *gin.Context) {
	params := usage.LogQueryParams{
		Page:   intQueryDefault(c, "page", 1),
		Size:   intQueryDefault(c, "size", 50),
		Days:   intQueryDefault(c, "days", 7),
		APIKey: strings.TrimSpace(c.Query("api_key")),
		Model:  strings.TrimSpace(c.Query("model")),
		Status: strings.TrimSpace(c.Query("status")),
	}

	result, err := usage.QueryLogs(params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	filters, err := usage.QueryFilters(params.Days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	stats, err := usage.QueryStats(params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Build name maps from config and auth store.
	keyNameMap, channelNameMap, authIndexChannelMap := h.buildNameMaps()

	// Enrich log items with resolved names
	for i := range result.Items {
		item := &result.Items[i]
		if item.APIKeyName == "" {
			if name, ok := keyNameMap[item.APIKey]; ok {
				item.APIKeyName = name
			}
		}
		// Prefer the current auth-index derived channel name so renamed OAuth
		// channels are reflected in logs immediately without rewriting history.
		if name, ok := authIndexChannelMap[item.AuthIndex]; ok && strings.TrimSpace(name) != "" {
			item.ChannelName = name
			continue
		}
		// Fall back to source-based mapping when the stored log channel is empty.
		if item.ChannelName == "" {
			if name, ok := channelNameMap[item.Source]; ok {
				item.ChannelName = name
			}
		}
	}

	// Enrich filter options with key names
	filters.APIKeyNames = make(map[string]string, len(filters.APIKeys))
	for _, key := range filters.APIKeys {
		if name, ok := keyNameMap[key]; ok {
			filters.APIKeyNames[key] = name
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"items":   result.Items,
		"total":   result.Total,
		"page":    result.Page,
		"size":    result.Size,
		"filters": filters,
		"stats":   stats,
	})
}

// buildNameMaps builds three maps from the current config/auth store:
//  1. keyNameMap:          user-facing api_key → display name
//  2. channelNameMap:      source/api_key/email → channel name
//  3. authIndexChannelMap: auth_index → current channel name
func (h *Handler) buildNameMaps() (keyNameMap, channelNameMap, authIndexChannelMap map[string]string) {
	keyNameMap = make(map[string]string)
	channelNameMap = make(map[string]string)
	authIndexChannelMap = make(map[string]string)

	// User-facing API key names from SQLite
	for _, row := range usage.ListAPIKeys() {
		if row.Key != "" && row.Name != "" {
			keyNameMap[row.Key] = row.Name
		}
	}

	cfg := h.cfg
	if cfg != nil {
		for _, k := range cfg.GeminiKey {
			if k.APIKey != "" && k.Name != "" {
				channelNameMap[k.APIKey] = k.Name
			}
		}
		for _, k := range cfg.ClaudeKey {
			if k.APIKey != "" && k.Name != "" {
				channelNameMap[k.APIKey] = k.Name
			}
		}
		for _, k := range cfg.CodexKey {
			if k.APIKey != "" && k.Name != "" {
				channelNameMap[k.APIKey] = k.Name
			}
		}
		// Vertex keys: no Name field, skip

		// OpenAI compatibility: provider name applies to all its API keys
		for _, provider := range cfg.OpenAICompatibility {
			if provider.Name == "" {
				continue
			}
			for _, entry := range provider.APIKeyEntries {
				if entry.APIKey != "" {
					channelNameMap[entry.APIKey] = provider.Name
				}
			}
		}
	}

	if h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if auth == nil {
				continue
			}
			channel := strings.TrimSpace(auth.ChannelName())
			if channel == "" {
				continue
			}
			auth.EnsureIndex()
			if idx := strings.TrimSpace(auth.Index); idx != "" {
				authIndexChannelMap[idx] = channel
			}
			if accountType, account := auth.AccountInfo(); strings.EqualFold(accountType, "oauth") {
				if source := strings.TrimSpace(account); source != "" {
					channelNameMap[source] = channel
				}
			}
			if email := strings.TrimSpace(authEmail(auth)); email != "" {
				channelNameMap[email] = channel
			}
		}
	}

	return
}

func intQueryDefault(c *gin.Context, key string, def int) int {
	v := strings.TrimSpace(c.Query(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return def
	}
	return n
}

// GetLogContent returns the stored request/response content for a single log entry.
func (h *Handler) GetLogContent(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log id"})
		return
	}

	result, err := usage.QueryLogContent(id)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			c.JSON(http.StatusNotFound, gin.H{"error": "log entry not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

// GetPublicUsageLogs returns paginated request log entries for a specific API key.
// This is a public endpoint (no management key required) that strips sensitive
// fields (source/auth_index/channel_name) before returning.
func (h *Handler) GetPublicUsageLogs(c *gin.Context) {
	apiKey := strings.TrimSpace(c.Query("api_key"))
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api_key parameter is required"})
		return
	}

	params := usage.LogQueryParams{
		Page:   intQueryDefault(c, "page", 1),
		Size:   intQueryDefault(c, "size", 50),
		Days:   intQueryDefault(c, "days", 7),
		APIKey: apiKey,
		Model:  strings.TrimSpace(c.Query("model")),
		Status: strings.TrimSpace(c.Query("status")),
	}

	result, err := usage.QueryLogs(params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	stats, err := usage.QueryStats(params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// SECURITY: Strip sensitive fields from public response
	for i := range result.Items {
		result.Items[i].Source = ""
		result.Items[i].AuthIndex = ""
		result.Items[i].ChannelName = ""
		result.Items[i].APIKey = ""
		result.Items[i].APIKeyName = ""
	}

	// Model filter options (scoped to this api_key via QueryFilters with key filter)
	models, _ := usage.QueryModelsForKey(apiKey, params.Days)

	c.JSON(http.StatusOK, gin.H{
		"items": result.Items,
		"total": result.Total,
		"page":  result.Page,
		"size":  result.Size,
		"stats": stats,
		"filters": gin.H{
			"models": models,
		},
	})
}

// GetPublicUsageChartData returns pre-aggregated chart data for a specific API key.
// This is a public endpoint (no management key required) that provides lightweight
// daily series and model distribution data for rendering charts.
func (h *Handler) GetPublicUsageChartData(c *gin.Context) {
	apiKey := strings.TrimSpace(c.Query("api_key"))
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api_key parameter is required"})
		return
	}

	days := intQueryDefault(c, "days", 7)

	daily, err := usage.QueryDailySeries(apiKey, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if daily == nil {
		daily = []usage.DailySeriesPoint{}
	}

	models, err := usage.QueryModelDistribution(apiKey, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if models == nil {
		models = []usage.ModelDistributionPoint{}
	}

	// Also fetch stats for KPI cards
	stats, err := usage.QueryStats(usage.LogQueryParams{APIKey: apiKey, Days: days})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"daily_series":       daily,
		"model_distribution": models,
		"stats":              stats,
	})
}

// GetPublicLogContent returns the stored request/response content for a single log entry,
// but only if it belongs to the specified API key. This is a public endpoint.
func (h *Handler) GetPublicLogContent(c *gin.Context) {
	apiKey := strings.TrimSpace(c.Query("api_key"))
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api_key parameter is required"})
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log id"})
		return
	}

	result, err := usage.QueryLogContentForKey(id, apiKey)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			c.JSON(http.StatusNotFound, gin.H{"error": "log entry not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

// GetUsageChartData returns pre-aggregated chart data for the management portal.
// It applies an optional apiKey filter.
func (h *Handler) GetUsageChartData(c *gin.Context) {
	apiKey := strings.TrimSpace(c.Query("api_key"))
	days := intQueryDefault(c, "days", 7)

	daily, err := usage.QueryDailySeries(apiKey, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if daily == nil {
		daily = []usage.DailySeriesPoint{}
	}

	models, err := usage.QueryModelDistribution(apiKey, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if models == nil {
		models = []usage.ModelDistributionPoint{}
	}

	hourlyTokens, hourlyModels, err := usage.QueryHourlySeries(apiKey, 24)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if hourlyTokens == nil {
		hourlyTokens = []usage.HourlyTokenPoint{}
	}
	if hourlyModels == nil {
		hourlyModels = []usage.HourlyModelPoint{}
	}

	// API Key distribution (only when not filtered by a single key)
	var apikeyDist []usage.APIKeyDistributionPoint
	if apiKey == "" {
		apikeyDist, err = usage.QueryAPIKeyDistribution(days)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Fallback: for older logs where api_key_name was not yet stored,
		// enrich with display names from the current config.
		keyNameMap, _, _ := h.buildNameMaps()
		for i := range apikeyDist {
			if apikeyDist[i].Name == "" {
				if name, ok := keyNameMap[apikeyDist[i].APIKey]; ok {
					apikeyDist[i].Name = name
				}
			}
		}
	}
	if apikeyDist == nil {
		apikeyDist = []usage.APIKeyDistributionPoint{}
	}

	c.JSON(http.StatusOK, gin.H{
		"daily_series":        daily,
		"model_distribution":  models,
		"hourly_tokens":       hourlyTokens,
		"hourly_models":       hourlyModels,
		"apikey_distribution": apikeyDist,
	})
}

// GetEntityUsageStats returns aggregated statistics grouped by source or auth_index
func (h *Handler) GetEntityUsageStats(c *gin.Context) {
	apiKey := strings.TrimSpace(c.Query("api_key"))
	days := intQueryDefault(c, "days", 7)

	sourceStats, err := usage.QueryEntityStats(apiKey, days, "source")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if sourceStats == nil {
		sourceStats = []usage.EntityStatPoint{}
	}

	authIndexStats, err := usage.QueryEntityStats(apiKey, days, "auth_index")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if authIndexStats == nil {
		authIndexStats = []usage.EntityStatPoint{}
	}

	c.JSON(http.StatusOK, gin.H{
		"source":     sourceStats,
		"auth_index": authIndexStats,
	})
}
