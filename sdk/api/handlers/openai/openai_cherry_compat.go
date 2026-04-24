package openai

import (
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	openAIImageModelID             = "gpt-image-2"
	openAICherryImageCompatModelID = "gptimage-2"
)

func isCherryStudioRequest(c *gin.Context) bool {
	if c == nil {
		return false
	}
	userAgent := strings.ToLower(strings.TrimSpace(c.GetHeader("User-Agent")))
	return strings.Contains(userAgent, "cherryai") || strings.Contains(userAgent, "cherry studio")
}

func presentedImageModelID(c *gin.Context, modelID string) string {
	if isCherryStudioRequest(c) && strings.EqualFold(strings.TrimSpace(modelID), openAIImageModelID) {
		return openAICherryImageCompatModelID
	}
	return modelID
}

func normalizeImageModelAlias(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if strings.EqualFold(modelID, openAICherryImageCompatModelID) {
		return openAIImageModelID
	}
	return modelID
}
