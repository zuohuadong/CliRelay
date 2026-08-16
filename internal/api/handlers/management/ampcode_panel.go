package management

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func (h *Handler) GetAmpCode(c *gin.Context) {
	h.mu.Lock()
	upstreamURL := h.cfg.AmpCode.UpstreamURL
	forceModelMappings := h.cfg.AmpCode.ForceModelMappings
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{
		"upstream-url":         upstreamURL,
		"upstreamUrl":          upstreamURL,
		"force-model-mappings": forceModelMappings,
		"forceModelMappings":   forceModelMappings,
	})
}

func (h *Handler) PutAmpCodeUpstreamURL(c *gin.Context) {
	h.updateAmpCodeString(c, func(value string) {
		h.cfg.AmpCode.UpstreamURL = value
	})
}

func (h *Handler) DeleteAmpCodeUpstreamURL(c *gin.Context) {
	h.mu.Lock()
	h.cfg.AmpCode.UpstreamURL = ""
	h.persistLocked(c)
	h.mu.Unlock()
}

func (h *Handler) PutAmpCodeUpstreamAPIKey(c *gin.Context) {
	h.updateAmpCodeString(c, func(value string) {
		h.cfg.AmpCode.UpstreamAPIKey = value
	})
}

func (h *Handler) DeleteAmpCodeUpstreamAPIKey(c *gin.Context) {
	h.mu.Lock()
	h.cfg.AmpCode.UpstreamAPIKey = ""
	h.persistLocked(c)
	h.mu.Unlock()
}

func (h *Handler) GetAmpCodeModelMappings(c *gin.Context) {
	h.mu.Lock()
	mappings := append([]config.AmpModelMapping(nil), h.cfg.AmpCode.ModelMappings...)
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{
		"model-mappings": mappings,
		"modelMappings":  mappings,
	})
}

func (h *Handler) PutAmpCodeModelMappings(c *gin.Context) {
	h.saveAmpCodeModelMappings(c)
}

func (h *Handler) PatchAmpCodeModelMappings(c *gin.Context) {
	h.saveAmpCodeModelMappings(c)
}

func (h *Handler) DeleteAmpCodeModelMappings(c *gin.Context) {
	var body struct {
		Value []string `json:"value"`
	}
	if c.Request != nil && c.Request.Body != nil && c.Request.ContentLength != 0 {
		data, err := c.GetRawData()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
			return
		}
		if len(strings.TrimSpace(string(data))) > 0 && json.Unmarshal(data, &body) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}

	remove := make(map[string]struct{}, len(body.Value))
	for _, from := range body.Value {
		if from = strings.TrimSpace(from); from != "" {
			remove[from] = struct{}{}
		}
	}

	h.mu.Lock()
	if len(remove) == 0 {
		h.cfg.AmpCode.ModelMappings = nil
	} else {
		kept := h.cfg.AmpCode.ModelMappings[:0]
		for _, mapping := range h.cfg.AmpCode.ModelMappings {
			if _, ok := remove[mapping.From]; !ok {
				kept = append(kept, mapping)
			}
		}
		h.cfg.AmpCode.ModelMappings = kept
	}
	h.persistLocked(c)
	h.mu.Unlock()
}

func (h *Handler) PutAmpCodeForceModelMappings(c *gin.Context) {
	var body struct {
		Value *bool `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	h.cfg.AmpCode.ForceModelMappings = *body.Value
	h.persistLocked(c)
	h.mu.Unlock()
}

func (h *Handler) updateAmpCodeString(c *gin.Context, set func(string)) {
	var body struct {
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	set(strings.TrimSpace(*body.Value))
	h.persistLocked(c)
	h.mu.Unlock()
}

func (h *Handler) saveAmpCodeModelMappings(c *gin.Context) {
	mappings, ok := decodeAmpCodeModelMappings(c)
	if !ok {
		return
	}
	h.mu.Lock()
	h.cfg.AmpCode.ModelMappings = mappings
	h.persistLocked(c)
	h.mu.Unlock()
}

func decodeAmpCodeModelMappings(c *gin.Context) ([]config.AmpModelMapping, bool) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return nil, false
	}
	var mappings []config.AmpModelMapping
	if err = json.Unmarshal(data, &mappings); err != nil {
		var body struct {
			Value []config.AmpModelMapping `json:"value"`
		}
		if err = json.Unmarshal(data, &body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return nil, false
		}
		mappings = body.Value
	}
	for i := range mappings {
		mappings[i].From = strings.TrimSpace(mappings[i].From)
		mappings[i].To = strings.TrimSpace(mappings[i].To)
		if mappings[i].From == "" || mappings[i].To == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "each model mapping requires from and to"})
			return nil, false
		}
	}
	return mappings, true
}
