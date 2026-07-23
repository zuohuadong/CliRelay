package auth

import (
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/contextorchestrator"
	"golang.org/x/sync/singleflight"
)

const (
	defaultRequestCompressionCacheTTL        = time.Hour
	defaultRequestCompressionCacheMaxEntries = 4096
)

type requestCompressionCacheEntry struct {
	capsule   contextorchestrator.Capsule
	expiresAt time.Time
	ttl       time.Duration
}

// requestCompressionCache stores only structured summaries keyed by a SHA-256
// history-prefix digest. Raw conversation history is never retained here.
type requestCompressionCache struct {
	mu      sync.Mutex
	entries map[string]requestCompressionCacheEntry
}

type requestCompressionRuntime struct {
	cache  requestCompressionCache
	flight singleflight.Group
}

type requestCompressionCacheWrite struct {
	profile    string
	digest     string
	capsule    contextorchestrator.Capsule
	ttl        time.Duration
	maxEntries int
}

func newRequestCompressionRuntime() *requestCompressionRuntime {
	return &requestCompressionRuntime{
		cache: requestCompressionCache{entries: make(map[string]requestCompressionCacheEntry)},
	}
}

func requestCompressionCacheKey(profile, digest string) string {
	return profile + "\x00" + digest
}

func (c *requestCompressionCache) getLongest(profile string, digests []string) (contextorchestrator.Capsule, int, bool) {
	if c == nil || profile == "" || len(digests) == 0 {
		return contextorchestrator.Capsule{}, 0, false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for count := len(digests); count > 0; count-- {
		key := requestCompressionCacheKey(profile, digests[count-1])
		entry, ok := c.entries[key]
		if !ok {
			continue
		}
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
			continue
		}
		entry.expiresAt = now.Add(entry.ttl)
		c.entries[key] = entry
		return entry.capsule, count, true
	}
	return contextorchestrator.Capsule{}, 0, false
}

func (c *requestCompressionCache) set(write requestCompressionCacheWrite) {
	if c == nil || write.profile == "" || write.digest == "" || !write.capsule.Valid() {
		return
	}
	if write.ttl <= 0 {
		write.ttl = defaultRequestCompressionCacheTTL
	}
	if write.maxEntries <= 0 {
		write.maxEntries = defaultRequestCompressionCacheMaxEntries
	}
	now := time.Now()
	key := requestCompressionCacheKey(write.profile, write.digest)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= write.maxEntries {
		c.evictOneLocked(now)
	}
	c.entries[key] = requestCompressionCacheEntry{
		capsule:   contextorchestrator.NormalizeCapsule(write.capsule),
		expiresAt: now.Add(write.ttl),
		ttl:       write.ttl,
	}
}

func (c *requestCompressionCache) evictOneLocked(now time.Time) {
	oldestKey := ""
	var oldestExpiry time.Time
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
			return
		}
		if oldestKey == "" || entry.expiresAt.Before(oldestExpiry) {
			oldestKey = key
			oldestExpiry = entry.expiresAt
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}
