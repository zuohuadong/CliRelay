package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const (
	responseAffinityTTL         = time.Hour
	responseAffinityMaxEntries  = 32768
	responseAffinityMetadataKey = "__cliproxy_previous_response_affinity"
)

type responseAffinityEntry struct {
	authID    string
	provider  string
	scope     string
	expiresAt time.Time
}

type responseAffinityBinding struct {
	auth     *Auth
	authID   string
	provider string
	scope    string
}

type responseAffinityCache struct {
	mu         sync.Mutex
	entries    map[string]responseAffinityEntry
	homeAuths  map[string]*Auth
	ttl        time.Duration
	maxEntries int
}

func newResponseAffinityCache(ttl time.Duration, maxEntries int) *responseAffinityCache {
	if ttl <= 0 {
		ttl = responseAffinityTTL
	}
	if maxEntries <= 0 {
		maxEntries = responseAffinityMaxEntries
	}
	return &responseAffinityCache{
		entries:    make(map[string]responseAffinityEntry),
		homeAuths:  make(map[string]*Auth),
		ttl:        ttl,
		maxEntries: maxEntries,
	}
}

func (c *responseAffinityCache) GetAndRefresh(responseID string) (responseAffinityBinding, bool) {
	responseID = strings.TrimSpace(responseID)
	if c == nil || responseID == "" {
		return responseAffinityBinding{}, false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[responseID]
	if !ok {
		return responseAffinityBinding{}, false
	}
	if !now.Before(entry.expiresAt) {
		c.deleteEntryLocked(responseID)
		return responseAffinityBinding{}, false
	}
	entry.expiresAt = now.Add(c.ttl)
	c.entries[responseID] = entry
	var homeAuth *Auth
	if cachedAuth := c.homeAuths[entry.authID]; cachedAuth != nil {
		homeAuth = cachedAuth.Clone()
	}
	return responseAffinityBinding{
		auth:     homeAuth,
		authID:   entry.authID,
		provider: entry.provider,
		scope:    entry.scope,
	}, true
}

func (c *responseAffinityCache) Set(responseID string, binding responseAffinityBinding) {
	responseID = strings.TrimSpace(responseID)
	binding.authID = strings.TrimSpace(binding.authID)
	binding.provider = strings.TrimSpace(binding.provider)
	binding.scope = strings.TrimSpace(binding.scope)
	if c == nil || responseID == "" || binding.authID == "" {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[responseID]; ok && existing.authID != binding.authID {
		c.deleteEntryLocked(responseID)
	}
	if _, exists := c.entries[responseID]; !exists && len(c.entries) >= c.maxEntries {
		c.evictOneLocked(now)
	}
	c.entries[responseID] = responseAffinityEntry{
		authID:    binding.authID,
		provider:  binding.provider,
		scope:     binding.scope,
		expiresAt: now.Add(c.ttl),
	}
	if binding.auth != nil {
		c.homeAuths[binding.authID] = binding.auth.Clone()
	} else {
		delete(c.homeAuths, binding.authID)
	}
}

func (c *responseAffinityCache) evictOneLocked(now time.Time) {
	oldestID := ""
	var oldestExpiry time.Time
	for responseID, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			c.deleteEntryLocked(responseID)
			return
		}
		if oldestID == "" || entry.expiresAt.Before(oldestExpiry) {
			oldestID = responseID
			oldestExpiry = entry.expiresAt
		}
	}
	if oldestID != "" {
		c.deleteEntryLocked(oldestID)
	}
}

func (c *responseAffinityCache) deleteEntryLocked(responseID string) {
	entry, ok := c.entries[responseID]
	if !ok {
		return
	}
	delete(c.entries, responseID)
	for _, remaining := range c.entries {
		if remaining.authID == entry.authID {
			return
		}
	}
	delete(c.homeAuths, entry.authID)
}

func (c *responseAffinityCache) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	clear(c.homeAuths)
	c.mu.Unlock()
}

func (m *Manager) applyPreviousResponseAffinity(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) cliproxyexecutor.Options {
	if m == nil || m.responseAffinity == nil {
		return opts
	}
	previousResponseID := previousResponseID(req, opts)
	if previousResponseID == "" {
		return opts
	}
	entry, ok := m.responseAffinity.GetAndRefresh(previousResponseID)
	if !ok {
		return opts
	}
	currentScope := responseAffinityScope(opts)
	if entry.scope != "" && currentScope != entry.scope {
		return opts
	}
	if m.HomeEnabled() && (entry.scope == "" || entry.auth == nil) {
		return opts
	}
	metadata := cloneMetadata(opts.Metadata)
	metadata[cliproxyexecutor.PinnedAuthMetadataKey] = entry.authID
	metadata[responseAffinityMetadataKey] = previousResponseID
	opts.Metadata = metadata
	return opts
}

func previousResponseID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	for _, payload := range [][]byte{opts.OriginalRequest, req.Payload} {
		if len(payload) == 0 {
			continue
		}
		if responseID := strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()); responseID != "" {
			return responseID
		}
	}
	return ""
}

func newResponseAffinityBinding(auth *Auth, provider string, opts cliproxyexecutor.Options) responseAffinityBinding {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	return responseAffinityBinding{auth: auth, authID: authID, provider: provider, scope: responseAffinityScope(opts)}
}

func (m *Manager) rememberResponseAffinity(binding responseAffinityBinding, payload []byte) {
	if m == nil || m.responseAffinity == nil || binding.auth == nil || strings.TrimSpace(binding.authID) == "" {
		return
	}
	if !m.HomeEnabled() {
		binding.auth = nil
	}
	for _, responseID := range responseIDsFromPayload(payload) {
		m.responseAffinity.Set(responseID, binding)
	}
}

func responseAffinityScope(opts cliproxyexecutor.Options) string {
	metadata := opts.Metadata
	if _, ok := metadata[cliproxyexecutor.DerivedSessionIDMetadataKey]; ok {
		metadata = cloneMetadata(metadata)
		delete(metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
	}
	sessionID := ExtractSessionID(opts.Headers, opts.OriginalRequest, metadata)
	if sessionID == "" || strings.HasPrefix(sessionID, "msg:") {
		return ""
	}
	return strings.Join([]string{
		sessionID,
		responseAffinityCallerFingerprint(opts.Headers),
		contextStringValue(opts.Metadata["allowed-channels"]),
		contextStringValue(opts.Metadata["allowed-channel-groups"]),
		contextStringValue(opts.Metadata[cliproxyexecutor.RouteGroupMetadataKey]),
	}, "\x00")
}

func responseAffinityCallerFingerprint(headers http.Header) string {
	credentials := strings.Join([]string{
		strings.TrimSpace(headers.Get("Authorization")),
		strings.TrimSpace(headers.Get("X-Goog-Api-Key")),
		strings.TrimSpace(headers.Get("X-Api-Key")),
	}, "\x00")
	if credentials == "\x00\x00" {
		return ""
	}
	digest := sha256.Sum256([]byte(credentials))
	return hex.EncodeToString(digest[:16])
}

func responseIDsFromPayload(payload []byte) []string {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return nil
	}
	responseIDs := responseIDsFromJSON(payload)
	for _, line := range bytes.Split(payload, []byte{'\n'}) {
		data := responseEventData(line)
		if len(data) == 0 {
			continue
		}
		responseIDs = appendUniqueResponseIDs(responseIDs, responseIDsFromJSON(data)...)
	}
	return responseIDs
}

func responseEventData(line []byte) []byte {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if bytes.Equal(data, []byte("[DONE]")) {
		return nil
	}
	return data
}

func responseIDsFromJSON(payload []byte) []string {
	if !gjson.ValidBytes(payload) {
		return nil
	}
	responseIDs := make([]string, 0, 2)
	if responseID := strings.TrimSpace(gjson.GetBytes(payload, "response.id").String()); responseID != "" {
		responseIDs = append(responseIDs, responseID)
	}
	if gjson.GetBytes(payload, "object").String() == "response" {
		responseIDs = appendUniqueResponseIDs(responseIDs, strings.TrimSpace(gjson.GetBytes(payload, "id").String()))
	}
	return responseIDs
}

func appendUniqueResponseIDs(existing []string, candidates ...string) []string {
	for _, candidate := range candidates {
		if candidate == "" || slices.Contains(existing, candidate) {
			continue
		}
		existing = append(existing, candidate)
	}
	return existing
}

func responseAffinityActive(opts cliproxyexecutor.Options) bool {
	return contextStringValue(opts.Metadata[responseAffinityMetadataKey]) != ""
}

func (m *Manager) responseAffinityHomeAuth(opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, bool, error) {
	if m == nil || m.responseAffinity == nil || !responseAffinityActive(opts) {
		return nil, nil, "", false, nil
	}
	responseID := contextStringValue(opts.Metadata[responseAffinityMetadataKey])
	entry, ok := m.responseAffinity.GetAndRefresh(responseID)
	if !ok || entry.auth == nil {
		return nil, nil, "", true, previousResponseAffinityError("the auth that created the previous response is no longer available")
	}
	if homeAuthAlreadyTried(tried, entry.authID) {
		return nil, nil, "", true, previousResponseAffinityError("the auth that created the previous response failed; refusing cross-account replay")
	}
	executor, provider, ok := m.responseAffinityExecutor(entry)
	if !ok {
		return nil, nil, "", true, previousResponseAffinityError("the executor for the previous response auth is unavailable")
	}
	return entry.auth.Clone(), executor, provider, true, nil
}

func (m *Manager) responseAffinityExecutor(entry responseAffinityBinding) (ProviderExecutor, string, bool) {
	provider := strings.TrimSpace(entry.provider)
	if provider == "" {
		provider = executorKeyFromAuth(entry.auth)
	}
	executor, ok := m.Executor(provider)
	if !ok && entry.auth.Attributes != nil && strings.TrimSpace(entry.auth.Attributes["base_url"]) != "" {
		executor, ok = m.Executor("openai-compatibility")
		if ok {
			provider = "openai-compatibility"
		}
	}
	return executor, provider, ok
}

type previousResponseUnavailableError struct {
	responseID string
	message    string
}

func (e *previousResponseUnavailableError) Error() string {
	message := strings.TrimSpace(e.message)
	if message == "" {
		message = "the auth that created the previous response is unavailable; replay the full transcript"
	}
	if responseID := strings.TrimSpace(e.responseID); responseID != "" {
		message = fmt.Sprintf("Previous response %s cannot be continued on its creating auth; replay the full transcript.", responseID)
	}
	return `{"error":{"message":` + strconv.Quote(message) + `,"type":"invalid_request_error","code":"previous_response_not_found","param":"previous_response_id"}}`
}

func (e *previousResponseUnavailableError) StatusCode() int { return http.StatusBadRequest }

func (e *previousResponseUnavailableError) IsRequestScoped() bool { return true }

func previousResponseAffinityError(message string) error {
	return &previousResponseUnavailableError{message: message}
}

func previousResponseReplayError(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	return &previousResponseUnavailableError{responseID: previousResponseID(req, opts)}
}
