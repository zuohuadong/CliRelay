package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	publicLookupRateLimitWindow       = time.Minute
	publicLookupRateLimitMaxRequests  = 60
	publicLookupRateLimitEntryMaxIdle = 10 * time.Minute
)

type publicLookupRateLimitEntry struct {
	count    int
	resetAt  time.Time
	lastSeen time.Time
}

func publicLookupNoStoreMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, private, max-age=0")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Next()
	}
}

func (s *Server) publicLookupRateLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil {
			c.Next()
			return
		}

		now := time.Now()
		key := strings.TrimSpace(c.ClientIP())
		if key == "" && c.Request != nil {
			key = strings.TrimSpace(c.Request.RemoteAddr)
		}
		if key == "" {
			key = "unknown"
		}
		if c != nil && c.Request != nil {
			userAgent := strings.TrimSpace(c.Request.UserAgent())
			if userAgent != "" {
				key += "|" + userAgent
			}
		}

		allowed, retryAfter := s.allowPublicLookupRequest(key, now)
		if allowed {
			c.Next()
			return
		}

		seconds := int(retryAfter / time.Second)
		if retryAfter%time.Second != 0 {
			seconds++
		}
		if seconds < 1 {
			seconds = 1
		}

		c.Header("Retry-After", strconvItoa(seconds))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error": "public lookup rate limit exceeded",
		})
	}
}

func (s *Server) allowPublicLookupRequest(key string, now time.Time) (bool, time.Duration) {
	s.publicLookupRateMu.Lock()
	defer s.publicLookupRateMu.Unlock()

	if s.publicLookupRate == nil {
		s.publicLookupRate = make(map[string]publicLookupRateLimitEntry)
	}

	if s.publicLookupRateLastCleanup.IsZero() || now.Sub(s.publicLookupRateLastCleanup) >= publicLookupRateLimitWindow {
		for candidate, entry := range s.publicLookupRate {
			if now.Sub(entry.lastSeen) > publicLookupRateLimitEntryMaxIdle {
				delete(s.publicLookupRate, candidate)
			}
		}
		s.publicLookupRateLastCleanup = now
	}

	entry := s.publicLookupRate[key]
	if entry.resetAt.IsZero() || !now.Before(entry.resetAt) {
		entry = publicLookupRateLimitEntry{
			count:    0,
			resetAt:  now.Add(publicLookupRateLimitWindow),
			lastSeen: now,
		}
	}

	entry.lastSeen = now
	if entry.count >= publicLookupRateLimitMaxRequests {
		s.publicLookupRate[key] = entry
		return false, time.Until(entry.resetAt)
	}

	entry.count++
	s.publicLookupRate[key] = entry
	return true, 0
}

func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	buf := [20]byte{}
	idx := len(buf)
	for v > 0 {
		idx--
		buf[idx] = byte('0' + v%10)
		v /= 10
	}
	if negative {
		idx--
		buf[idx] = '-'
	}
	return string(buf[idx:])
}
