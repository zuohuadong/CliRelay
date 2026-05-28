package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	log "github.com/sirupsen/logrus"
)

func accessMetadata(c *gin.Context) map[string]string {
	if c == nil {
		return nil
	}
	raw, exists := c.Get("accessMetadata")
	if !exists {
		return nil
	}
	m, ok := raw.(map[string]string)
	if !ok {
		return nil
	}
	return m
}

func apiKeyFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	raw, exists := c.Get("userApiKey")
	if !exists {
		return ""
	}
	s, _ := raw.(string)
	return s
}

func APIKeyQuotaMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		metadata := accessMetadata(c)
		if metadata == nil {
			c.Next()
			return
		}

		apiKey := apiKeyFromContext(c)
		if apiKey == "" {
			c.Next()
			return
		}

		if dailyLimitStr, ok := metadata["daily-limit"]; ok {
			dailyLimit, _ := strconv.Atoi(dailyLimitStr)
			if dailyLimit > 0 {
				reqCount, _, _ := usage.APIKeyUsageToday(apiKey)
				if reqCount >= dailyLimit {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
						"error": map[string]any{
							"message": "daily request limit exceeded for this API key",
							"type":    "quota_exceeded",
							"code":    "daily_limit_exceeded",
						},
					})
					return
				}
			}
		}

		if totalQuotaStr, ok := metadata["total-quota"]; ok {
			totalQuota, _ := strconv.Atoi(totalQuotaStr)
			if totalQuota > 0 {
				reqCount, _, _ := usage.APIKeyTotalUsage(apiKey)
				if reqCount >= totalQuota {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
						"error": map[string]any{
							"message": "total request quota exceeded for this API key",
							"type":    "quota_exceeded",
							"code":    "total_quota_exceeded",
						},
					})
					return
				}
			}
		}

		if spendingLimitStr, ok := metadata["spending-limit"]; ok {
			spendingLimit, _ := strconv.ParseFloat(spendingLimitStr, 64)
			if spendingLimit > 0 {
				_, _, cost := usage.APIKeyTotalUsage(apiKey)
				if cost >= spendingLimit {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
						"error": map[string]any{
							"message": "spending limit exceeded for this API key",
							"type":    "quota_exceeded",
							"code":    "spending_limit_exceeded",
						},
					})
					return
				}
			}
		}

		c.Next()
	}
}

func APIKeyRateLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		metadata := accessMetadata(c)
		if metadata == nil {
			c.Next()
			return
		}

		apiKey := apiKeyFromContext(c)
		if apiKey == "" {
			c.Next()
			return
		}

		if rpmLimitStr, ok := metadata["rpm-limit"]; ok {
			rpmLimit, _ := strconv.Atoi(rpmLimitStr)
			if rpmLimit > 0 {
				currentRPM := usage.APIKeyRPMCount(apiKey)
				if currentRPM >= rpmLimit {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
						"error": map[string]any{
							"message": "requests per minute limit exceeded for this API key",
							"type":    "rate_limit_exceeded",
							"code":    "rpm_limit_exceeded",
						},
					})
					return
				}
			}
		}

		if tpmLimitStr, ok := metadata["tpm-limit"]; ok {
			tpmLimit, _ := strconv.Atoi(tpmLimitStr)
			if tpmLimit > 0 {
				currentTPM := usage.APIKeyTPMCount(apiKey)
				if currentTPM >= tpmLimit {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
						"error": map[string]any{
							"message": "tokens per minute limit exceeded for this API key",
							"type":    "rate_limit_exceeded",
							"code":    "tpm_limit_exceeded",
						},
					})
					return
				}
			}
		}

		c.Next()
	}
}

var (
	concurrencyCounters sync.Map
)

type concurrencyCounter struct {
	current atomic.Int64
}

func APIKeyConcurrencyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		metadata := accessMetadata(c)
		if metadata == nil {
			c.Next()
			return
		}

		concurrencyLimitStr, ok := metadata["concurrency-limit"]
		if !ok {
			c.Next()
			return
		}
		concurrencyLimit, _ := strconv.Atoi(concurrencyLimitStr)
		if concurrencyLimit <= 0 {
			c.Next()
			return
		}

		apiKey := apiKeyFromContext(c)
		if apiKey == "" {
			c.Next()
			return
		}

		counterVal, _ := concurrencyCounters.LoadOrStore(apiKey, &concurrencyCounter{})
		counter := counterVal.(*concurrencyCounter)

		for {
			current := counter.current.Load()
			if current >= int64(concurrencyLimit) {
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"error": map[string]any{
						"message": "concurrency limit exceeded for this API key",
						"type":    "rate_limit_exceeded",
						"code":    "concurrency_limit_exceeded",
					},
				})
				return
			}
			if counter.current.CompareAndSwap(current, current+1) {
				break
			}
		}
		defer func() {
			counter.current.Add(-1)
		}()

		c.Next()
	}
}

func APIKeyModelAccessMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		metadata := accessMetadata(c)
		if metadata == nil {
			c.Next()
			return
		}

		allowedModelsStr, ok := metadata["allowed-models"]
		if !ok || allowedModelsStr == "" {
			c.Next()
			return
		}
		allowedModels := parseCommaList(allowedModelsStr)
		if len(allowedModels) == 0 {
			c.Next()
			return
		}

		model := extractModelFromRequest(c)
		if model == "" {
			c.Next()
			return
		}

		if !modelMatchesAny(model, allowedModels) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": map[string]any{
					"message": "model not allowed for this API key",
					"type":    "forbidden",
					"code":    "model_not_allowed",
					"model":   model,
				},
			})
			return
		}

		c.Next()
	}
}

func APIKeyChannelAccessMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		metadata := accessMetadata(c)
		if metadata == nil {
			c.Next()
			return
		}

		allowedChannelsStr, ok := metadata["allowed-channels"]
		if !ok || allowedChannelsStr == "" {
			c.Next()
			return
		}
		allowedChannels := parseCommaList(allowedChannelsStr)
		if len(allowedChannels) == 0 {
			c.Next()
			return
		}

		channelName := ""
		if raw, exists := c.Get("channelName"); exists {
			channelName, _ = raw.(string)
		}
		if channelName == "" {
			c.Next()
			return
		}

		if !stringInSlice(channelName, allowedChannels) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": map[string]any{
					"message": "channel not allowed for this API key",
					"type":    "forbidden",
					"code":    "channel_not_allowed",
					"channel": channelName,
				},
			})
			return
		}

		c.Next()
	}
}

func APIKeySystemPromptMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		metadata := accessMetadata(c)
		if metadata == nil {
			c.Next()
			return
		}

		systemPrompt, ok := metadata["system-prompt"]
		if !ok || strings.TrimSpace(systemPrompt) == "" {
			c.Next()
			return
		}

		if c.Request == nil || c.Request.Body == nil {
			c.Next()
			return
		}

		contentType := c.Request.Header.Get("Content-Type")
		if !strings.Contains(contentType, "application/json") {
			c.Next()
			return
		}

		bodyBytes, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			log.WithError(errRead).Debug("apikey enforcement: failed to read request body for system prompt injection")
			c.Next()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		c.Request.ContentLength = int64(len(bodyBytes))

		modified, errInject := injectSystemPrompt(bodyBytes, systemPrompt)
		if errInject != nil {
			log.WithError(errInject).Debug("apikey enforcement: failed to inject system prompt")
			c.Next()
			return
		}

		if modified != nil {
			c.Request.Body = io.NopCloser(bytes.NewReader(modified))
			c.Request.ContentLength = int64(len(modified))
		}

		c.Next()
	}
}

func injectSystemPrompt(body []byte, prompt string) ([]byte, error) {
	if len(body) == 0 {
		return nil, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil
	}

	messagesVal, ok := payload["messages"]
	if !ok {
		return nil, nil
	}

	messages, ok := messagesVal.([]any)
	if !ok || len(messages) == 0 {
		return nil, nil
	}

	firstMsg, ok := messages[0].(map[string]any)
	if !ok {
		return nil, nil
	}

	role, _ := firstMsg["role"].(string)
	if role == "system" {
		existingContent, _ := firstMsg["content"].(string)
		if existingContent != "" {
			firstMsg["content"] = prompt + "\n\n" + existingContent
		} else {
			firstMsg["content"] = prompt
		}
	} else {
		systemMsg := map[string]any{
			"role":    "system",
			"content": prompt,
		}
		messages = append([]any{systemMsg}, messages...)
		payload["messages"] = messages
	}

	return json.Marshal(payload)
}

func extractModelFromRequest(c *gin.Context) string {
	if c.Request == nil {
		return ""
	}

	switch c.Request.Method {
	case http.MethodGet:
		return strings.TrimSpace(c.Query("model"))
	case http.MethodPost:
		if c.Request.Body == nil {
			return ""
		}
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return ""
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		var payload map[string]any
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			return ""
		}
		model, _ := payload["model"].(string)
		return strings.TrimSpace(model)
	default:
		return ""
	}
}

func parseCommaList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func modelMatchesAny(model string, allowed []string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	for _, a := range allowed {
		a = strings.TrimSpace(strings.ToLower(a))
		if a == model {
			return true
		}
		if strings.HasSuffix(a, "*") {
			prefix := strings.TrimSuffix(a, "*")
			if strings.HasPrefix(model, prefix) {
				return true
			}
		}
	}
	return false
}

func stringInSlice(s string, slice []string) bool {
	s = strings.TrimSpace(s)
	for _, item := range slice {
		if strings.TrimSpace(item) == s {
			return true
		}
	}
	return false
}

// GetConcurrencySnapshot returns a list of API keys with their current
// concurrency count and the total in-flight request count.
func GetConcurrencySnapshot() ([]map[string]interface{}, int64) {
	var snapshots []map[string]interface{}
	var totalInFlight int64
	concurrencyCounters.Range(func(key, value interface{}) bool {
		counter := value.(*concurrencyCounter)
		current := counter.current.Load()
		if current <= 0 {
			return true
		}
		snapshots = append(snapshots, map[string]interface{}{
			"api_key":     key.(string),
			"concurrency": current,
		})
		totalInFlight += current
		return true
	})
	return snapshots, totalInFlight
}
