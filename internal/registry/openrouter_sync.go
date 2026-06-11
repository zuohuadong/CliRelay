package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	openRouterModelsURL          = "https://openrouter.ai/api/v1/models"
	openRouterDefaultSyncMinutes = 1440 // 24 hours
	openRouterMinSyncMinutes     = 60   // 1 hour minimum
	openRouterFetchTimeout       = 30 * time.Second
)

// OpenRouterSyncState tracks the current state of the OpenRouter sync process.
type OpenRouterSyncState struct {
	mu               sync.RWMutex
	enabled          bool
	intervalMinutes  int
	apiKey           string
	lastSyncAt       time.Time
	lastSuccessAt    time.Time
	lastError        string
	lastSeen         int
	lastAdded        int
	lastUpdated      int
	lastSkipped      int
	running          bool
	registeredModels map[string]*openRouterModelEntry
	cancel           context.CancelFunc
}

type openRouterModelEntry struct {
	ID              string
	Name            string
	ContextLength   int
	MaxCompletion   int
	PromptPrice     float64
	CompletionPrice float64
	RegisteredAt    time.Time
}

// openRouterModel represents a model entry from the OpenRouter API.
type openRouterModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	TopProvider struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
}

// openRouterModelsResponse represents the response from the OpenRouter models API.
type openRouterModelsResponse struct {
	Data []openRouterModel `json:"data"`
}

// OpenRouterSyncResult holds the result of a single sync operation.
type OpenRouterSyncResult struct {
	Seen    int `json:"seen"`
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

var (
	openRouterSync     *OpenRouterSyncState
	openRouterSyncOnce sync.Once
)

// GetOpenRouterSyncState returns the global OpenRouter sync state singleton.
func GetOpenRouterSyncState() *OpenRouterSyncState {
	openRouterSyncOnce.Do(func() {
		openRouterSync = &OpenRouterSyncState{
			intervalMinutes:  openRouterDefaultSyncMinutes,
			registeredModels: make(map[string]*openRouterModelEntry),
		}
	})
	return openRouterSync
}

// StartOpenRouterSync starts periodic OpenRouter model metadata synchronization.
// It is safe to call multiple times; only one sync loop will run.
func StartOpenRouterSync(ctx context.Context, enabled bool, intervalMinutes int, apiKey string) {
	state := GetOpenRouterSyncState()

	state.mu.Lock()
	// Stop any existing loop
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	state.enabled = enabled
	state.apiKey = apiKey
	if intervalMinutes < openRouterMinSyncMinutes {
		intervalMinutes = openRouterMinSyncMinutes
	}
	state.intervalMinutes = intervalMinutes
	state.mu.Unlock()

	if !enabled {
		log.Info("openrouter sync: disabled")
		return
	}

	syncCtx, cancel := context.WithCancel(ctx)
	state.mu.Lock()
	state.cancel = cancel
	state.mu.Unlock()

	go runOpenRouterSyncLoop(syncCtx, state)
}

// StopOpenRouterSync stops the background sync loop.
func StopOpenRouterSync() {
	state := GetOpenRouterSyncState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	state.enabled = false
}

// RunOpenRouterSyncOnce performs a single OpenRouter model sync and returns the result.
func RunOpenRouterSyncOnce() (*OpenRouterSyncResult, error) {
	state := GetOpenRouterSyncState()

	state.mu.RLock()
	apiKey := state.apiKey
	state.mu.RUnlock()

	state.mu.Lock()
	if state.running {
		state.mu.Unlock()
		return nil, fmt.Errorf("sync already in progress")
	}
	state.running = true
	state.mu.Unlock()

	defer func() {
		state.mu.Lock()
		state.running = false
		state.mu.Unlock()
	}()

	result, err := fetchAndRegisterOpenRouterModels(apiKey)
	now := time.Now()

	state.mu.Lock()
	state.lastSyncAt = now
	if err != nil {
		state.lastError = err.Error()
	} else {
		state.lastSuccessAt = now
		state.lastError = ""
		if result != nil {
			state.lastSeen = result.Seen
			state.lastAdded = result.Added
			state.lastUpdated = result.Updated
			state.lastSkipped = result.Skipped
		}
	}
	state.mu.Unlock()

	return result, err
}

// UpdateOpenRouterSyncConfig updates the sync configuration at runtime.
func UpdateOpenRouterSyncConfig(enabled bool, intervalMinutes int, apiKey string) {
	state := GetOpenRouterSyncState()

	state.mu.Lock()
	state.enabled = enabled
	state.apiKey = apiKey
	if intervalMinutes < openRouterMinSyncMinutes {
		intervalMinutes = openRouterMinSyncMinutes
	}
	state.intervalMinutes = intervalMinutes

	// Stop existing loop if running
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	state.mu.Unlock()

	if enabled {
		syncCtx, cancel := context.WithCancel(context.Background())
		state.mu.Lock()
		state.cancel = cancel
		state.mu.Unlock()
		go runOpenRouterSyncLoop(syncCtx, state)
	}
}

// GetOpenRouterSyncSnapshot returns a read-only snapshot of the sync state for API responses.
func GetOpenRouterSyncSnapshot() map[string]any {
	state := GetOpenRouterSyncState()
	state.mu.RLock()
	defer state.mu.RUnlock()

	return map[string]any{
		"enabled":          state.enabled,
		"interval_minutes": state.intervalMinutes,
		"last_sync_at":     formatTime(state.lastSyncAt),
		"last_success_at":  formatTime(state.lastSuccessAt),
		"last_error":       state.lastError,
		"last_seen":        state.lastSeen,
		"last_added":       state.lastAdded,
		"last_updated":     state.lastUpdated,
		"last_skipped":     state.lastSkipped,
		"running":          state.running,
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func runOpenRouterSyncLoop(ctx context.Context, state *OpenRouterSyncState) {
	// Initial sync
	result, err := fetchAndRegisterOpenRouterModels(state.apiKey)
	now := time.Now()

	state.mu.Lock()
	state.lastSyncAt = now
	state.running = false
	if err != nil {
		state.lastError = err.Error()
		log.Errorf("openrouter sync: initial sync failed: %v", err)
	} else {
		state.lastSuccessAt = now
		state.lastError = ""
		if result != nil {
			state.lastSeen = result.Seen
			state.lastAdded = result.Added
			state.lastUpdated = result.Updated
			state.lastSkipped = result.Skipped
		}
		log.Infof("openrouter sync: initial sync completed: seen=%d added=%d updated=%d skipped=%d",
			result.Seen, result.Added, result.Updated, result.Skipped)
	}
	intervalMinutes := state.intervalMinutes
	state.mu.Unlock()

	interval := time.Duration(intervalMinutes) * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state.mu.RLock()
			apiKey := state.apiKey
			currentInterval := state.intervalMinutes
			enabled := state.enabled
			state.mu.RUnlock()

			if !enabled {
				return
			}

			// Adjust ticker if interval changed
			newInterval := time.Duration(currentInterval) * time.Minute
			if newInterval != interval {
				ticker.Stop()
				ticker = time.NewTicker(newInterval)
				interval = newInterval
			}

			result, err := fetchAndRegisterOpenRouterModels(apiKey)
			syncNow := time.Now()

			state.mu.Lock()
			state.lastSyncAt = syncNow
			if err != nil {
				state.lastError = err.Error()
				log.Errorf("openrouter sync: periodic sync failed: %v", err)
			} else {
				state.lastSuccessAt = syncNow
				state.lastError = ""
				if result != nil {
					state.lastSeen = result.Seen
					state.lastAdded = result.Added
					state.lastUpdated = result.Updated
					state.lastSkipped = result.Skipped
				}
				log.Infof("openrouter sync: periodic sync completed: seen=%d added=%d updated=%d skipped=%d",
					result.Seen, result.Added, result.Updated, result.Skipped)
			}
			state.mu.Unlock()
		}
	}
}

func fetchAndRegisterOpenRouterModels(apiKey string) (*OpenRouterSyncResult, error) {
	client := &http.Client{Timeout: openRouterFetchTimeout}
	req, err := http.NewRequest("GET", openRouterModelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Debugf("openrouter sync: close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var modelsResp openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	return syncOpenRouterModels(GetOpenRouterSyncState(), modelsResp.Data, time.Now()), nil
}

func syncOpenRouterModels(state *OpenRouterSyncState, models []openRouterModel, now time.Time) *OpenRouterSyncResult {
	if state == nil {
		return &OpenRouterSyncResult{}
	}

	result := &OpenRouterSyncResult{}
	seenIDs := make(map[string]struct{}, len(models))

	for i := range models {
		m := &models[i]
		if m.ID == "" {
			continue
		}
		result.Seen++
		seenIDs[m.ID] = struct{}{}

		promptPrice := parsePrice(m.Pricing.Prompt)
		completionPrice := parsePrice(m.Pricing.Completion)

		entry := &openRouterModelEntry{
			ID:              m.ID,
			Name:            m.Name,
			ContextLength:   m.ContextLength,
			MaxCompletion:   m.TopProvider.MaxCompletionTokens,
			PromptPrice:     promptPrice,
			CompletionPrice: completionPrice,
			RegisteredAt:    now,
		}

		state.mu.Lock()
		if state.registeredModels == nil {
			state.registeredModels = make(map[string]*openRouterModelEntry)
		}
		existing, exists := state.registeredModels[m.ID]
		state.mu.Unlock()

		if exists {
			// Check if model metadata changed
			if modelChanged(existing, entry) {
				state.mu.Lock()
				state.registeredModels[m.ID] = entry
				state.mu.Unlock()
				result.Updated++
			} else {
				result.Skipped++
			}
		} else {
			state.mu.Lock()
			state.registeredModels[m.ID] = entry
			state.mu.Unlock()
			result.Added++
		}
	}

	reconcileRemovedOpenRouterModels(state, seenIDs)

	return result
}

func reconcileRemovedOpenRouterModels(state *OpenRouterSyncState, seenIDs map[string]struct{}) {
	if state == nil {
		return
	}
	state.mu.Lock()
	removedIDs := make([]string, 0)
	for id := range state.registeredModels {
		if _, stillPresent := seenIDs[id]; stillPresent {
			continue
		}
		delete(state.registeredModels, id)
		removedIDs = append(removedIDs, id)
	}
	state.mu.Unlock()

	for _, id := range removedIDs {
		GetGlobalRegistry().UnregisterClient(openRouterSyncClientID(id))
	}
}

func modelChanged(old, new *openRouterModelEntry) bool {
	return old.ContextLength != new.ContextLength ||
		old.MaxCompletion != new.MaxCompletion ||
		old.PromptPrice != new.PromptPrice ||
		old.CompletionPrice != new.CompletionPrice ||
		old.Name != new.Name
}

func openRouterSyncClientID(modelID string) string {
	return "openrouter-sync:" + modelID
}

// parsePrice converts an OpenRouter price string (per-token in USD) to a float64.
func parsePrice(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
