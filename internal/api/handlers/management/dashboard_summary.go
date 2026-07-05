package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

func (h *Handler) GetDashboardSummary(c *gin.Context) {
	filters := usageFiltersFromQuery(c)

	cfg := h.cfg
	geminiCount := 0
	claudeCount := 0
	codexCount := 0
	vertexCount := 0
	openaiCount := 0
	bigmodelCount := 0
	astronCodeCount := 0
	agnesCount := 0
	iflowCount := 0
	if cfg != nil {
		geminiCount = len(cfg.GeminiKey)
		claudeCount = len(cfg.ClaudeKey)
		codexCount = len(cfg.CodexKey)
		vertexCount = len(cfg.VertexCompatAPIKey)
		openaiCount = len(cfg.OpenAICompatibility)
		cfg.MigrateBigModelCodingFromOpenAICompatibility()
		cfg.SanitizeBigModelCoding()
		bigmodelCount = len(cfg.BigModelCodingAPIKey)
		cfg.MigrateAstronCodeFromOpenAICompatibility()
		cfg.SanitizeAstronCode()
		astronCodeCount = len(cfg.AstronCodeAPIKey)
		cfg.MigrateAgnesFromOpenAICompatibility()
		cfg.SanitizeAgnes()
		agnesCount = len(cfg.AgnesAPIKey)
	}

	iflowAuthCount := 0
	if h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "iflow") {
				iflowAuthCount++
			}
		}
	}
	iflowCount = iflowAuthCount

	authFileCount := 0
	if h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if h.buildAuthFileEntry(auth) != nil {
				authFileCount++
			}
		}
	}

	// API key count from the usage DB
	apiKeyCount := 0
	if keys := usage.ListAPIKeys(); keys != nil {
		apiKeyCount = len(keys)
	}

	totals := usageTotals{}
	requestVolume := []gin.H{}
	successRateTrend := []gin.H{}
	totalTokensTrend := []gin.H{}
	totalCostTrend := []gin.H{}
	failedRequestsTrend := []gin.H{}
	throughputSeries := []gin.H{}
	if db, ok := h.usageDB(); ok {
		defer func() { _ = db.Close() }()
		totals = queryUsageTotals(db, filters)
		for _, point := range queryUsageDailySeries(db, filters) {
			requests := int64FromGinValue(point["requests"])
			failed := int64FromGinValue(point["failed_requests"])
			successRate := float64(0)
			if requests > 0 {
				successRate = float64(requests-failed) / float64(requests) * 100
			}
			label := point["date"]
			requestVolume = append(requestVolume, gin.H{"date": label, "label": label, "value": requests, "requests": requests})
			successRateTrend = append(successRateTrend, gin.H{"date": label, "label": label, "value": successRate, "success_rate": successRate})
			totalTokensTrend = append(totalTokensTrend, gin.H{"date": label, "label": label, "value": point["total_tokens"], "total_tokens": point["total_tokens"]})
			totalCostTrend = append(totalCostTrend, gin.H{"date": label, "label": label, "value": point["total_cost"], "total_cost": point["total_cost"]})
			failedRequestsTrend = append(failedRequestsTrend, gin.H{"date": label, "label": label, "value": failed, "failed_requests": failed})
		}
		throughputSeries = queryUsageHourlyThroughput(db, filters)
	}
	successRate := float64(0)
	if totals.Total > 0 {
		successRate = float64(totals.Success) / float64(totals.Total) * 100
	}

	c.JSON(http.StatusOK, gin.H{
		"kpi": gin.H{
			"total_requests":   totals.Total,
			"success_requests": totals.Success,
			"failed_requests":  totals.Failed,
			"success_rate":     successRate,
			"input_tokens":     totals.InputTokens,
			"output_tokens":    totals.OutputTokens,
			"reasoning_tokens": totals.ReasoningTokens,
			"cached_tokens":    totals.CachedTokens,
			"total_tokens":     totals.TotalTokens,
			"total_cost":       totals.TotalCost,
		},
		"counts": gin.H{
			"api_keys":         apiKeyCount,
			"providers_total":  geminiCount + claudeCount + codexCount + vertexCount + openaiCount + bigmodelCount + astronCodeCount + agnesCount + iflowCount,
			"gemini_keys":      geminiCount,
			"claude_keys":      claudeCount,
			"codex_keys":       codexCount,
			"vertex_keys":      vertexCount,
			"openai_providers": openaiCount,
			"bigmodel_keys":    bigmodelCount,
			"astron_code_keys": astronCodeCount,
			"agnes_keys":       agnesCount,
			"iflow_keys":       iflowCount,
			"auth_files":       authFileCount,
		},
		"trends": gin.H{
			"request_volume":    requestVolume,
			"success_rate":      successRateTrend,
			"total_tokens":      totalTokensTrend,
			"total_cost":        totalCostTrend,
			"failed_requests":   failedRequestsTrend,
			"throughput_series": throughputSeries,
		},
		"meta": gin.H{
			"generated_at": time.Now().UTC().Format(time.RFC3339),
		},
		"days": filters.Days,
	})
}

func int64FromGinValue(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return 0
	}
}
