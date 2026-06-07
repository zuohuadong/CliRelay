package vision

import (
	"strings"
)

// Processor handles vision/image preprocessing for different providers
type Processor struct {
	registry *ContextRegistry
}

// NewProcessor creates a new vision processor
func NewProcessor(registry *ContextRegistry) *Processor {
	return &Processor{registry: registry}
}

// EnrichWithHistory adds historical image context to the current request
// when the current message doesn't contain images but previous turns did
func (p *Processor) EnrichWithHistory(sessionID string, currentContent []map[string]any, currentTurnIndex int) []map[string]any {
	if p == nil || p.registry == nil {
		return currentContent
	}

	// If current content already has images, no enrichment needed
	if HasImageContent(currentContent) {
		return currentContent
	}

	// Look up historical images from previous turns
	historyImages := p.registry.Lookup(sessionID)
	if len(historyImages) == 0 {
		return currentContent
	}

	// Add historical image references to current content
	enriched := make([]map[string]any, len(currentContent))
	copy(enriched, currentContent)

	for _, img := range historyImages {
		if img.TurnIndex >= currentTurnIndex {
			continue // Only include images from earlier turns
		}

		url := img.DataURL
		if url == "" {
			url = img.URL
		}
		if url == "" {
			continue
		}

		enriched = append(enriched, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": url},
		})
	}

	return enriched
}

// RegisterImagesFromContent registers images found in message content
func (p *Processor) RegisterImagesFromContent(sessionID string, content []map[string]any, turnIndex int) {
	if p == nil || p.registry == nil {
		return
	}

	urls := ExtractImageURLs(content)
	for i, url := range urls {
		entry := ImageEntry{
			ID:        string(rune('a' + i)),
			TurnIndex: turnIndex,
		}
		if IsDataURL(url) {
			entry.DataURL = url
		} else {
			entry.URL = url
		}
		// Extract mime type from data URL
		if strings.HasPrefix(url, "data:") {
			parts := strings.SplitN(url[5:], ";", 2)
			if len(parts) > 0 {
				entry.MimeType = parts[0]
			}
		}
		p.registry.Register(sessionID, entry)
	}
}
