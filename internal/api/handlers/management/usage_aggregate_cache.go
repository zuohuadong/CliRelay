package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	usageAggregateCacheDefaultTTL = 10 * time.Second
	usageAggregateCacheMaxEntries = 32
)

type usageAggregateKind string

const (
	usageAggregateChart  usageAggregateKind = "chart"
	usageAggregateEntity usageAggregateKind = "entity"
)

type usageAggregateCacheEntry struct {
	payload    gin.H
	expiresAt  time.Time
	insertedAt time.Time
}

type canonicalUsageFilters struct {
	Page        int      `json:"page"`
	Size        int      `json:"size"`
	Days        int      `json:"days"`
	APIKey      string   `json:"api_key"`
	Model       string   `json:"model"`
	Channel     string   `json:"channel"`
	Status      string   `json:"status"`
	Failed      string   `json:"failed"`
	Start       string   `json:"start"`
	End         string   `json:"end"`
	AuthIndexes []string `json:"auth_indexes"`
	Sources     []string `json:"sources"`
}

func usageAggregateCacheKey(kind usageAggregateKind, filters usageFilters) string {
	canonical := canonicalUsageFilters{
		Page:        filters.Page,
		Size:        filters.Size,
		Days:        filters.Days,
		APIKey:      strings.TrimSpace(filters.APIKey),
		Model:       strings.TrimSpace(filters.Model),
		Channel:     strings.TrimSpace(filters.Channel),
		Status:      strings.ToLower(strings.TrimSpace(filters.Status)),
		Failed:      strings.ToLower(strings.TrimSpace(filters.Failed)),
		Start:       strings.TrimSpace(filters.Start),
		End:         strings.TrimSpace(filters.End),
		AuthIndexes: canonicalUsageFilterValues(filters.AuthIndexes),
		Sources:     canonicalUsageFilterValues(filters.Sources),
	}
	raw, _ := json.Marshal(canonical)
	digest := sha256.Sum256(raw)
	return string(kind) + ":" + hex.EncodeToString(digest[:])
}

func canonicalUsageFilterValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (h *Handler) loadUsageAggregate(ctx context.Context, kind usageAggregateKind, filters usageFilters, build func(context.Context) (gin.H, error)) (gin.H, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if h == nil {
		return build(ctx)
	}

	key := usageAggregateCacheKey(kind, filters)
	for attempt := 0; attempt < 2; attempt++ {
		generation, payload, found := h.lookupUsageAggregateCache(key)
		if found {
			return payload, nil
		}
		flightKey := fmt.Sprintf("%d:%s", generation, key)
		resultChannel := h.usageAggregateFlight.DoChan(flightKey, func() (any, error) {
			if cached, ok := h.lookupUsageAggregateCacheGeneration(key, generation); ok {
				return cached, nil
			}
			built, err := build(ctx)
			if err != nil {
				return nil, err
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			h.storeUsageAggregateCache(key, generation, built)
			return built, nil
		})

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-resultChannel:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if result.Err != nil {
				sharedContextEnded := errors.Is(result.Err, context.Canceled) || errors.Is(result.Err, context.DeadlineExceeded)
				if attempt == 0 && sharedContextEnded {
					continue
				}
				return nil, result.Err
			}
			payload, ok := result.Val.(gin.H)
			if !ok {
				return nil, fmt.Errorf("unexpected usage aggregate payload type %T", result.Val)
			}
			return payload, nil
		}
	}
	return nil, context.Canceled
}

func (h *Handler) lookupUsageAggregateCache(key string) (uint64, gin.H, bool) {
	h.usageAggregateCacheMu.Lock()
	defer h.usageAggregateCacheMu.Unlock()
	generation := h.usageAggregateCacheGeneration
	payload, found := h.lookupUsageAggregateCacheLocked(key, h.usageAggregateCacheNowLocked())
	return generation, payload, found
}

func (h *Handler) lookupUsageAggregateCacheGeneration(key string, generation uint64) (gin.H, bool) {
	h.usageAggregateCacheMu.Lock()
	defer h.usageAggregateCacheMu.Unlock()
	if h.usageAggregateCacheGeneration != generation {
		return nil, false
	}
	return h.lookupUsageAggregateCacheLocked(key, h.usageAggregateCacheNowLocked())
}

func (h *Handler) lookupUsageAggregateCacheLocked(key string, now time.Time) (gin.H, bool) {
	entry, found := h.usageAggregateCache[key]
	if !found {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		delete(h.usageAggregateCache, key)
		return nil, false
	}
	return entry.payload, true
}

func (h *Handler) storeUsageAggregateCache(key string, generation uint64, payload gin.H) {
	h.usageAggregateCacheMu.Lock()
	defer h.usageAggregateCacheMu.Unlock()
	if h.usageAggregateCacheGeneration != generation {
		return
	}
	if h.usageAggregateCache == nil {
		h.usageAggregateCache = make(map[string]usageAggregateCacheEntry)
	}
	now := h.usageAggregateCacheNowLocked()
	for cachedKey, entry := range h.usageAggregateCache {
		if !now.Before(entry.expiresAt) {
			delete(h.usageAggregateCache, cachedKey)
		}
	}
	if _, exists := h.usageAggregateCache[key]; !exists && len(h.usageAggregateCache) >= usageAggregateCacheMaxEntries {
		oldestKey := ""
		var oldestTime time.Time
		for cachedKey, entry := range h.usageAggregateCache {
			if oldestKey == "" || entry.insertedAt.Before(oldestTime) {
				oldestKey = cachedKey
				oldestTime = entry.insertedAt
			}
		}
		delete(h.usageAggregateCache, oldestKey)
	}
	ttl := h.usageAggregateCacheTTL
	if ttl <= 0 {
		ttl = usageAggregateCacheDefaultTTL
	}
	h.usageAggregateCache[key] = usageAggregateCacheEntry{
		payload:    payload,
		expiresAt:  now.Add(ttl),
		insertedAt: now,
	}
}

func (h *Handler) invalidateUsageAggregateCache() {
	if h == nil {
		return
	}
	h.usageAggregateCacheMu.Lock()
	h.usageAggregateCache = nil
	h.usageAggregateCacheGeneration++
	h.usageAggregateCacheMu.Unlock()
}

func (h *Handler) usageAggregateCacheNowLocked() time.Time {
	if h.usageAggregateCacheNow != nil {
		return h.usageAggregateCacheNow()
	}
	return time.Now()
}
