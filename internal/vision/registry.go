package vision

import (
	"sync"
	"time"
)

// ImageEntry represents a stored image reference in the context registry
type ImageEntry struct {
	ID        string    `json:"id"`
	DataURL   string    `json:"data_url,omitempty"`
	URL       string    `json:"url,omitempty"`
	MimeType  string    `json:"mime_type,omitempty"`
	AddedAt   time.Time `json:"added_at"`
	TurnIndex int       `json:"turn_index"`
}

// ContextRegistry tracks images across conversation turns
type ContextRegistry struct {
	mu            sync.RWMutex
	entries       map[string][]ImageEntry // keyed by conversation/session ID
	maxAge        time.Duration
	maxPerSession int
}

// NewContextRegistry creates a new image context registry
func NewContextRegistry() *ContextRegistry {
	return &ContextRegistry{
		entries:       make(map[string][]ImageEntry),
		maxAge:        30 * time.Minute,
		maxPerSession: 20,
	}
}

// Register adds an image to the context registry for a given session
func (r *ContextRegistry) Register(sessionID string, entry ImageEntry) {
	if r == nil || sessionID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	entry.AddedAt = time.Now()
	entries := r.entries[sessionID]
	entries = append(entries, entry)

	// Enforce max per session
	if len(entries) > r.maxPerSession {
		entries = entries[len(entries)-r.maxPerSession:]
	}
	r.entries[sessionID] = entries
}

// Lookup retrieves all images for a session
func (r *ContextRegistry) Lookup(sessionID string) []ImageEntry {
	if r == nil || sessionID == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := r.entries[sessionID]
	if len(entries) == 0 {
		return nil
	}

	// Filter out expired entries
	result := make([]ImageEntry, 0, len(entries))
	now := time.Now()
	for _, e := range entries {
		if now.Sub(e.AddedAt) < r.maxAge {
			result = append(result, e)
		}
	}
	return result
}

// LookupByIndex retrieves images from a specific turn index
func (r *ContextRegistry) LookupByIndex(sessionID string, turnIndex int) []ImageEntry {
	if r == nil || sessionID == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []ImageEntry
	for _, e := range r.entries[sessionID] {
		if e.TurnIndex == turnIndex {
			result = append(result, e)
		}
	}
	return result
}

// Clear removes all entries for a session
func (r *ContextRegistry) Clear(sessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, sessionID)
}

// PurgeExpired removes all expired entries across all sessions
func (r *ContextRegistry) PurgeExpired() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	for sessionID, entries := range r.entries {
		valid := make([]ImageEntry, 0, len(entries))
		for _, e := range entries {
			if now.Sub(e.AddedAt) < r.maxAge {
				valid = append(valid, e)
			}
		}
		if len(valid) == 0 {
			delete(r.entries, sessionID)
		} else {
			r.entries[sessionID] = valid
		}
	}
}

// SetMaxAge configures the maximum age for entries
func (r *ContextRegistry) SetMaxAge(d time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxAge = d
}

// SetMaxPerSession configures the maximum number of images per session
func (r *ContextRegistry) SetMaxPerSession(n int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxPerSession = n
}
