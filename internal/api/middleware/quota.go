package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	log "github.com/sirupsen/logrus"
)

// concurrencyTracker tracks the number of in-flight requests per API key.
var concurrencyTracker sync.Map // map[string]*atomic.Int64

// getOrCreateCounter returns the atomic counter for the given API key.
func getOrCreateCounter(apiKey string) *atomic.Int64 {
	if v, loaded := concurrencyTracker.Load(apiKey); loaded {
		return v.(*atomic.Int64)
	}
	counter := &atomic.Int64{}
	actual, _ := concurrencyTracker.LoadOrStore(apiKey, counter)
	return actual.(*atomic.Int64)
}

// QuotaMiddleware enforces daily-limit, total-quota, and concurrency-limit
// restrictions for authenticated API keys. It reads the limits from the
// accessMetadata set by the auth provider during authentication.
//
// This middleware MUST be placed after AuthMiddleware (which sets "apiKey"
// and "accessMetadata" in the gin context) and before route handlers.
//
// Only POST requests are checked (GET /models etc. don't consume quota).
func QuotaMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only enforce on POST requests (actual API calls)
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		// Get the authenticated API key
		apiKeyVal, exists := c.Get("apiKey")
		if !exists {
			c.Next()
			return
		}
		apiKey, ok := apiKeyVal.(string)
		if !ok || apiKey == "" {
			c.Next()
			return
		}

		// Get access metadata containing limits
		metadataVal, exists := c.Get("accessMetadata")
		if !exists {
			c.Next()
			return
		}
		metadata, ok := metadataVal.(map[string]string)
		if !ok {
			c.Next()
			return
		}

		// Parse limits from metadata
		dailyLimit := parseIntMetadata(metadata, "daily-limit")
		totalQuota := parseIntMetadata(metadata, "total-quota")
		concurrencyLimit := parseIntMetadata(metadata, "concurrency-limit")

		// No limits configured — skip all checks
		if dailyLimit <= 0 && totalQuota <= 0 && concurrencyLimit <= 0 {
			c.Next()
			return
		}

		// --- Daily limit check ---
		if dailyLimit > 0 {
			todayCount, err := usage.CountTodayByKey(apiKey)
			if err != nil {
				log.Warnf("quota: failed to query daily usage for key %s: %v", maskKey(apiKey), err)
				// On error, allow the request through to avoid blocking service
			} else if todayCount >= int64(dailyLimit) {
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"error": map[string]interface{}{
						"message": fmt.Sprintf("daily request limit (%d) exceeded for this API key", dailyLimit),
						"type":    "rate_limit_exceeded",
						"code":    "daily_limit_exceeded",
					},
				})
				return
			}
		}

		// --- Total quota check ---
		if totalQuota > 0 {
			totalCount, err := usage.CountTotalByKey(apiKey)
			if err != nil {
				log.Warnf("quota: failed to query total usage for key %s: %v", maskKey(apiKey), err)
			} else if totalCount >= int64(totalQuota) {
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"error": map[string]interface{}{
						"message": fmt.Sprintf("total request quota (%d) exhausted for this API key", totalQuota),
						"type":    "rate_limit_exceeded",
						"code":    "total_quota_exceeded",
					},
				})
				return
			}
		}

		// --- Concurrency limit check ---
		if concurrencyLimit > 0 {
			counter := getOrCreateCounter(apiKey)
			current := counter.Add(1)
			defer counter.Add(-1)

			if current > int64(concurrencyLimit) {
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"error": map[string]interface{}{
						"message": fmt.Sprintf("concurrent request limit (%d) exceeded for this API key", concurrencyLimit),
						"type":    "rate_limit_exceeded",
						"code":    "concurrency_limit_exceeded",
					},
				})
				return
			}
		}

		c.Next()
	}
}

// parseIntMetadata reads an integer from the metadata map.
// Returns 0 if the key is missing or the value is not a valid integer.
func parseIntMetadata(metadata map[string]string, key string) int {
	v, ok := metadata[key]
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0
	}
	return n
}

// maskKey returns a masked version of the API key for logging.
func maskKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}
