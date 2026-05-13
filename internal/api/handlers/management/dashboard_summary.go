package management

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

func (h *Handler) GetDashboardSummary(c *gin.Context) {
	days := 7
	if parsed, err := strconv.Atoi(c.DefaultQuery("days", "7")); err == nil && parsed > 0 {
		days = parsed
	}

	cfg := h.cfg
	geminiCount := 0
	claudeCount := 0
	codexCount := 0
	vertexCount := 0
	openaiCount := 0
	if cfg != nil {
		geminiCount = len(cfg.GeminiKey)
		claudeCount = len(cfg.ClaudeKey)
		codexCount = len(cfg.CodexKey)
		vertexCount = len(cfg.VertexCompatAPIKey)
		openaiCount = len(cfg.OpenAICompatibility)
	}

	authFileCount := 0
	if h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if h.buildAuthFileEntry(auth) != nil {
				authFileCount++
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"kpi": gin.H{
			"total_requests":   0,
			"success_requests": 0,
			"failed_requests":  0,
			"success_rate":     0,
			"input_tokens":     0,
			"output_tokens":    0,
			"reasoning_tokens": 0,
			"cached_tokens":    0,
			"total_tokens":     0,
			"total_cost":       0,
		},
		"counts": gin.H{
			"api_keys":         0,
			"providers_total":  geminiCount + claudeCount + codexCount + vertexCount + openaiCount,
			"gemini_keys":      geminiCount,
			"claude_keys":      claudeCount,
			"codex_keys":       codexCount,
			"vertex_keys":      vertexCount,
			"openai_providers": openaiCount,
			"auth_files":       authFileCount,
		},
		"trends": gin.H{
			"request_volume":    []gin.H{},
			"success_rate":      []gin.H{},
			"total_tokens":      []gin.H{},
			"total_cost":        []gin.H{},
			"failed_requests":   []gin.H{},
			"throughput_series": []gin.H{},
		},
		"meta": gin.H{
			"generated_at": time.Now().UTC().Format(time.RFC3339),
		},
		"days": days,
	})
}
