package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// GetOpenRouterSync returns the current OpenRouter sync state.
func (h *Handler) GetOpenRouterSync(c *gin.Context) {
	snapshot := registry.GetOpenRouterSyncSnapshot()
	c.JSON(http.StatusOK, snapshot)
}

// PutOpenRouterSync updates the OpenRouter sync configuration.
func (h *Handler) PutOpenRouterSync(c *gin.Context) {
	var body struct {
		Enabled         *bool  `json:"enabled"`
		IntervalMinutes *int   `json:"interval_minutes"`
		APIKey          string `json:"api_key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()

	if cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config not available"})
		return
	}

	enabled := cfg.OpenRouterSyncEnabled
	intervalMinutes := cfg.OpenRouterSyncIntervalMinutes
	apiKey := cfg.OpenRouterAPIKey

	if body.Enabled != nil {
		enabled = *body.Enabled
		cfg.OpenRouterSyncEnabled = enabled
	}
	if body.IntervalMinutes != nil {
		intervalMinutes = *body.IntervalMinutes
		cfg.OpenRouterSyncIntervalMinutes = intervalMinutes
	}
	if body.APIKey != "" {
		apiKey = body.APIKey
		cfg.OpenRouterAPIKey = apiKey
	}

	// Persist config changes
	if err := config.SaveConfigPreserveComments(h.configFilePath, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save config"})
		return
	}

	// Update the running sync configuration
	registry.UpdateOpenRouterSyncConfig(enabled, intervalMinutes, apiKey)

	snapshot := registry.GetOpenRouterSyncSnapshot()
	c.JSON(http.StatusOK, snapshot)
}

// RunOpenRouterSync triggers a manual OpenRouter model sync.
func (h *Handler) RunOpenRouterSync(c *gin.Context) {
	result, err := registry.RunOpenRouterSyncOnce()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
			"state": registry.GetOpenRouterSyncSnapshot(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"result": result,
		"state":  registry.GetOpenRouterSyncSnapshot(),
	})
}
