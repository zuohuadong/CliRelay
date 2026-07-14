package management

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	managementAuthCacheDefaultTTL = 5 * time.Minute
	managementAuthCacheMaxEntries = 64
)

type managementAuthCacheEntry struct {
	expiresAt  time.Time
	insertedAt time.Time
}

type managementAuthSnapshot struct {
	allowRemote   bool
	secretHash    string
	shareToken    string
	localPassword string
	envSecret     string
	generation    uint64
}

func (h *Handler) managementAuthSnapshot() managementAuthSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := managementAuthSnapshot{
		localPassword: h.localPassword,
		envSecret:     h.envSecret,
		generation:    h.managementAuthGeneration,
	}
	if h.cfg != nil {
		snapshot.allowRemote = h.cfg.RemoteManagement.AllowRemote
		snapshot.secretHash = h.cfg.RemoteManagement.SecretKey
		snapshot.shareToken = strings.TrimSpace(h.cfg.RemoteManagement.ShareToken)
	}
	if h.allowRemoteOverride {
		snapshot.allowRemote = true
	}
	return snapshot
}

func (h *Handler) managementAuthGenerationStillCurrent(generation uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.managementAuthGeneration == generation
}

func (h *Handler) managementAuthHashStillCurrent(generation uint64, secretHash string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.managementAuthGeneration != generation || h.cfg == nil {
		return false
	}
	return h.cfg.RemoteManagement.SecretKey == secretHash
}

func (h *Handler) verifyHashedManagementPassword(generation uint64, secretHash, provided string) bool {
	if h == nil || secretHash == "" || provided == "" {
		return false
	}
	cacheKey, cacheEnabled := h.managementAuthCacheDigest(generation, secretHash, provided)
	if !cacheEnabled {
		return h.verifyManagementPasswordWithoutCache(generation, secretHash, provided)
	}
	if h.lookupManagementAuthCache(cacheKey) {
		return h.managementAuthHashStillCurrent(generation, secretHash)
	}

	flightKey := hex.EncodeToString(cacheKey[:])
	result, _, _ := h.managementAuthFlight.Do(flightKey, func() (any, error) {
		if h.lookupManagementAuthCache(cacheKey) {
			return h.managementAuthHashStillCurrent(generation, secretHash), nil
		}
		if !h.verifyManagementPasswordWithoutCache(generation, secretHash, provided) {
			return false, nil
		}
		h.storeManagementAuthCache(cacheKey)
		return true, nil
	})
	valid, _ := result.(bool)
	return valid && h.managementAuthHashStillCurrent(generation, secretHash)
}

func (h *Handler) verifyManagementPasswordWithoutCache(generation uint64, secretHash, provided string) bool {
	verifier := h.managementPasswordVerifier
	if verifier == nil {
		verifier = bcrypt.CompareHashAndPassword
	}
	if verifier([]byte(secretHash), []byte(provided)) != nil {
		return false
	}
	return h.managementAuthHashStillCurrent(generation, secretHash)
}

func (h *Handler) managementAuthCacheDigest(generation uint64, secretHash, provided string) ([sha256.Size]byte, bool) {
	h.managementAuthCacheMu.Lock()
	defer h.managementAuthCacheMu.Unlock()
	if !h.managementAuthCacheKeyReady {
		if _, err := rand.Read(h.managementAuthCacheHMACKey[:]); err != nil {
			return [sha256.Size]byte{}, false
		}
		h.managementAuthCacheKeyReady = true
	}
	hasher := hmac.New(sha256.New, h.managementAuthCacheHMACKey[:])
	var generationBytes [8]byte
	binary.BigEndian.PutUint64(generationBytes[:], generation)
	_, _ = hasher.Write(generationBytes[:])
	_, _ = hasher.Write([]byte(secretHash))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(provided))
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, true
}

func (h *Handler) lookupManagementAuthCache(key [sha256.Size]byte) bool {
	h.managementAuthCacheMu.Lock()
	defer h.managementAuthCacheMu.Unlock()
	now := h.managementAuthCacheNowTimeLocked()
	h.purgeExpiredManagementAuthCacheLocked(now)
	entry, found := h.managementAuthCache[key]
	if !found {
		return false
	}
	if !now.Before(entry.expiresAt) {
		delete(h.managementAuthCache, key)
		return false
	}
	return true
}

func (h *Handler) storeManagementAuthCache(key [sha256.Size]byte) {
	h.managementAuthCacheMu.Lock()
	defer h.managementAuthCacheMu.Unlock()
	if h.managementAuthCache == nil {
		h.managementAuthCache = make(map[[sha256.Size]byte]managementAuthCacheEntry)
	}
	now := h.managementAuthCacheNowTimeLocked()
	h.purgeExpiredManagementAuthCacheLocked(now)
	if _, exists := h.managementAuthCache[key]; !exists && len(h.managementAuthCache) >= managementAuthCacheMaxEntries {
		var oldestKey [sha256.Size]byte
		var oldestTime time.Time
		foundOldest := false
		for cachedKey, entry := range h.managementAuthCache {
			if !foundOldest || entry.insertedAt.Before(oldestTime) {
				oldestKey = cachedKey
				oldestTime = entry.insertedAt
				foundOldest = true
			}
		}
		if foundOldest {
			delete(h.managementAuthCache, oldestKey)
		}
	}
	ttl := h.managementAuthCacheTTL
	if ttl <= 0 {
		ttl = managementAuthCacheDefaultTTL
	}
	h.managementAuthCache[key] = managementAuthCacheEntry{
		expiresAt:  now.Add(ttl),
		insertedAt: now,
	}
}

func (h *Handler) invalidateManagementAuthCache() {
	if h == nil {
		return
	}
	h.managementAuthCacheMu.Lock()
	h.managementAuthCache = nil
	for index := range h.managementAuthCacheHMACKey {
		h.managementAuthCacheHMACKey[index] = 0
	}
	h.managementAuthCacheKeyReady = false
	h.managementAuthCacheMu.Unlock()
}

func (h *Handler) startManagementAuthCacheCleanup() {
	go func() {
		ticker := time.NewTicker(managementAuthCacheDefaultTTL)
		defer ticker.Stop()
		for range ticker.C {
			h.purgeExpiredManagementAuthCache()
		}
	}()
}

func (h *Handler) purgeExpiredManagementAuthCache() {
	if h == nil {
		return
	}
	h.managementAuthCacheMu.Lock()
	h.purgeExpiredManagementAuthCacheLocked(h.managementAuthCacheNowTimeLocked())
	h.managementAuthCacheMu.Unlock()
}

func (h *Handler) purgeExpiredManagementAuthCacheLocked(now time.Time) {
	for cachedKey, entry := range h.managementAuthCache {
		if !now.Before(entry.expiresAt) {
			delete(h.managementAuthCache, cachedKey)
		}
	}
}

func (h *Handler) managementAuthCacheNowTimeLocked() time.Time {
	if h.managementAuthCacheNow != nil {
		return h.managementAuthCacheNow()
	}
	return time.Now()
}
