package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

// GetModelPrices returns all configured model pricing entries.
func (h *Handler) GetModelPrices(c *gin.Context) {
	prices := usage.ListModelPrices()
	if prices == nil {
		prices = []usage.ModelPriceRow{}
	}
	c.JSON(http.StatusOK, gin.H{"data": prices})
}

// PutModelPrice creates or updates pricing for a single model.
func (h *Handler) PutModelPrice(c *gin.Context) {
	model := strings.TrimSpace(c.Param("model"))
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}

	var req struct {
		Mode            string  `json:"mode"`
		InputPricePerM  float64 `json:"input_price_per_million"`
		OutputPricePerM float64 `json:"output_price_per_million"`
		CachedPricePerM float64 `json:"cached_price_per_million"`
		PricePerCall    float64 `json:"price_per_call"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "token"
	}

	row := usage.ModelPriceRow{
		Model:           model,
		Mode:            mode,
		InputPricePerM:  req.InputPricePerM,
		OutputPricePerM: req.OutputPricePerM,
		CachedPricePerM: req.CachedPricePerM,
		PricePerCall:    req.PricePerCall,
	}
	if err := usage.SetModelPrice(row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"model": row})
}

// DeleteModelPrice removes pricing for a model.
func (h *Handler) DeleteModelPrice(c *gin.Context) {
	model := strings.TrimSpace(c.Param("model"))
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}
	if err := usage.DeleteModelPrice(model); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": model})
}

// RefreshModelPrices triggers an immediate fetch of official platform pricing
// from LiteLLM and upserts it into the model_prices table.
func (h *Handler) RefreshModelPrices(c *gin.Context) {
	added, updated, skipped := usage.RefreshModelPricesFromRemote()
	c.JSON(http.StatusOK, gin.H{
		"added":   added,
		"updated": updated,
		"skipped": skipped,
	})
}
