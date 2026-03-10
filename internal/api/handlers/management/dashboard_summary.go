package management

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// GetDashboardSummary is a lightweight endpoint that returns only the
// counts and KPIs needed by the frontend dashboard page, avoiding
// the transfer of the full usage / config payloads.
//
// GET /v0/management/dashboard-summary?days=7
func (h *Handler) GetDashboardSummary(c *gin.Context) {
	cfg := h.cfg

	// ── Provider key counts ──
	geminiCount := 0
	claudeCount := 0
	codexCount := 0
	vertexCount := 0
	openaiCount := 0
	authFileCount := 0
	apiKeyCount := 0

	if cfg != nil {
		geminiCount = len(cfg.GeminiKey)
		claudeCount = len(cfg.ClaudeKey)
		codexCount = len(cfg.CodexKey)
		vertexCount = len(cfg.VertexCompatAPIKey)
		openaiCount = len(cfg.OpenAICompatibility)
		apiKeyCount = len(cfg.APIKeyEntries)
	}

	if h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if entry := h.buildAuthFileEntry(auth); entry != nil {
				authFileCount++
			}
		}
	}

	providerTotal := geminiCount + claudeCount + codexCount + vertexCount + openaiCount

	// ── Usage KPIs (time-filtered) ──
	daysStr := c.DefaultQuery("days", "7")
	days := 7
	if v, err := parsePositiveInt(daysStr); err == nil && v > 0 {
		days = v
	}

	var totalRequests, failedRequests int
	var inputTokens, outputTokens, reasoningTokens, cachedTokens, totalTokens int64

	if h.usageStats != nil {
		snapshot := h.usageStats.Snapshot()
		now := time.Now()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		cutoff := today.AddDate(0, 0, -(days - 1))

		for _, api := range snapshot.APIs {
			for _, model := range api.Models {
				for _, detail := range model.Details {
					if detail.Timestamp.Before(cutoff) {
						continue
					}
					totalRequests++
					if detail.Failed {
						failedRequests++
					}
					inputTokens += detail.Tokens.InputTokens
					outputTokens += detail.Tokens.OutputTokens
					reasoningTokens += detail.Tokens.ReasoningTokens
					cachedTokens += detail.Tokens.CachedTokens
					totalTokens += detail.Tokens.TotalTokens
				}
			}
		}
	}

	successRequests := totalRequests - failedRequests
	successRate := float64(0)
	if totalRequests > 0 {
		successRate = float64(successRequests) / float64(totalRequests) * 100
	}

	c.JSON(http.StatusOK, gin.H{
		"kpi": gin.H{
			"total_requests":   totalRequests,
			"success_requests": successRequests,
			"failed_requests":  failedRequests,
			"success_rate":     successRate,
			"input_tokens":     inputTokens,
			"output_tokens":    outputTokens,
			"reasoning_tokens": reasoningTokens,
			"cached_tokens":    cachedTokens,
			"total_tokens":     totalTokens,
		},
		"counts": gin.H{
			"api_keys":         apiKeyCount,
			"providers_total":  providerTotal,
			"gemini_keys":      geminiCount,
			"claude_keys":      claudeCount,
			"codex_keys":       codexCount,
			"vertex_keys":      vertexCount,
			"openai_providers": openaiCount,
			"auth_files":       authFileCount,
		},
		"days": days,
	})
}

func parsePositiveInt(s string) (int, error) {
	var v int
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}
