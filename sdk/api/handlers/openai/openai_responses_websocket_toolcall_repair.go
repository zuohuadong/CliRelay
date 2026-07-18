package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	websocketToolOutputCacheMaxPerSession = 256
	websocketToolOutputCacheTTL           = 30 * time.Minute
)

var defaultWebsocketToolOutputCache = newWebsocketToolOutputCacheWithBytes(websocketToolOutputCacheTTL, websocketToolOutputCacheMaxPerSession, 8<<20)
var defaultWebsocketToolCallCache = newWebsocketToolOutputCacheWithBytes(websocketToolOutputCacheTTL, websocketToolOutputCacheMaxPerSession, 8<<20)
var defaultWebsocketToolSessionRefs = newWebsocketToolSessionRefCounter()

type websocketToolOutputCache struct {
	mu            sync.Mutex
	ttl           time.Duration
	maxPerSession int
	maxBytes      int64
	sessions      map[string]*websocketToolOutputSession
}

type websocketToolOutputSession struct {
	lastSeen time.Time
	outputs  map[string]json.RawMessage
	order    []string
	bytes    int64
	maxBytes int64
}

func newWebsocketToolOutputCache(ttl time.Duration, maxPerSession int) *websocketToolOutputCache {
	return newWebsocketToolOutputCacheWithBytes(ttl, maxPerSession, 0)
}

func newWebsocketToolOutputCacheWithBytes(ttl time.Duration, maxPerSession int, maxBytes int64) *websocketToolOutputCache {
	if ttl < 0 {
		ttl = websocketToolOutputCacheTTL
	}
	if maxPerSession <= 0 {
		maxPerSession = websocketToolOutputCacheMaxPerSession
	}
	return &websocketToolOutputCache{
		ttl:           ttl,
		maxPerSession: maxPerSession,
		maxBytes:      maxBytes,
		sessions:      make(map[string]*websocketToolOutputSession),
	}
}

func (c *websocketToolOutputCache) record(sessionKey string, callID string, item json.RawMessage) {
	sessionKey = strings.TrimSpace(sessionKey)
	callID = strings.TrimSpace(callID)
	if sessionKey == "" || callID == "" || c == nil {
		return
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)

	session, ok := c.sessions[sessionKey]
	if !ok || session == nil {
		session = &websocketToolOutputSession{
			lastSeen: now,
			outputs:  make(map[string]json.RawMessage),
		}
		c.sessions[sessionKey] = session
	}
	session.lastSeen = now
	limit := session.maxBytes
	if limit <= 0 {
		limit = c.maxBytes
	}
	if old, exists := session.outputs[callID]; exists {
		session.bytes -= int64(len(old))
	}
	if limit > 0 && int64(len(item)) > limit {
		delete(session.outputs, callID)
		removeWebsocketToolCacheOrder(&session.order, callID)
		return
	}

	if _, exists := session.outputs[callID]; !exists {
		session.order = append(session.order, callID)
	}
	session.outputs[callID] = append(json.RawMessage(nil), item...)
	session.bytes += int64(len(item))

	for len(session.order) > c.maxPerSession || (limit > 0 && session.bytes > limit) {
		evict := session.order[0]
		session.order = session.order[1:]
		session.bytes -= int64(len(session.outputs[evict]))
		delete(session.outputs, evict)
	}
}

func removeWebsocketToolCacheOrder(order *[]string, callID string) {
	if order == nil {
		return
	}
	for i, value := range *order {
		if value == callID {
			*order = append((*order)[:i], (*order)[i+1:]...)
			return
		}
	}
}

func (c *websocketToolOutputCache) setSessionMaxBytes(sessionKey string, maxBytes int64) {
	if c == nil || strings.TrimSpace(sessionKey) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	session := c.sessions[sessionKey]
	if session == nil {
		session = &websocketToolOutputSession{lastSeen: time.Now(), outputs: make(map[string]json.RawMessage)}
		c.sessions[sessionKey] = session
	}
	session.maxBytes = maxBytes
	for maxBytes > 0 && session.bytes > maxBytes && len(session.order) > 0 {
		evict := session.order[0]
		session.order = session.order[1:]
		session.bytes -= int64(len(session.outputs[evict]))
		delete(session.outputs, evict)
	}
}

func (c *websocketToolOutputCache) get(sessionKey string, callID string) (json.RawMessage, bool) {
	sessionKey = strings.TrimSpace(sessionKey)
	callID = strings.TrimSpace(callID)
	if sessionKey == "" || callID == "" || c == nil {
		return nil, false
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)

	session, ok := c.sessions[sessionKey]
	if !ok || session == nil {
		return nil, false
	}
	session.lastSeen = now
	item, ok := session.outputs[callID]
	if !ok || len(item) == 0 {
		return nil, false
	}
	return append(json.RawMessage(nil), item...), true
}

func (c *websocketToolOutputCache) cleanupLocked(now time.Time) {
	if c == nil || c.ttl <= 0 {
		return
	}

	for key, session := range c.sessions {
		if session == nil {
			delete(c.sessions, key)
			continue
		}
		if now.Sub(session.lastSeen) > c.ttl {
			delete(c.sessions, key)
		}
	}
}

func (c *websocketToolOutputCache) deleteSession(sessionKey string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.sessions, sessionKey)
}

func (c *websocketToolOutputCache) clearSessionItems(sessionKey string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	session := c.sessions[sessionKey]
	if session == nil {
		return
	}
	session.outputs = make(map[string]json.RawMessage)
	session.order = nil
	session.bytes = 0
	session.lastSeen = time.Now()
}

func websocketDownstreamSessionKey(req *http.Request) string {
	if req == nil {
		return ""
	}
	if requestID := strings.TrimSpace(req.Header.Get("X-Client-Request-Id")); requestID != "" {
		return requestID
	}
	if raw := strings.TrimSpace(req.Header.Get("X-Codex-Turn-Metadata")); raw != "" {
		if sessionID := strings.TrimSpace(gjson.Get(raw, "session_id").String()); sessionID != "" {
			return sessionID
		}
	}
	if sessionID := strings.TrimSpace(req.Header.Get("Session-Id")); sessionID != "" {
		return sessionID
	}
	if sessionID := strings.TrimSpace(req.Header.Get("Session_id")); sessionID != "" {
		return sessionID
	}
	return ""
}

type websocketToolSessionRefCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newWebsocketToolSessionRefCounter() *websocketToolSessionRefCounter {
	return &websocketToolSessionRefCounter{counts: make(map[string]int)}
}

func (c *websocketToolSessionRefCounter) acquire(sessionKey string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.counts[sessionKey]++
}

func (c *websocketToolSessionRefCounter) release(sessionKey string) bool {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	count := c.counts[sessionKey]
	if count <= 1 {
		delete(c.counts, sessionKey)
		return true
	}
	c.counts[sessionKey] = count - 1
	return false
}

func retainResponsesWebsocketToolCaches(sessionKey string) {
	if defaultWebsocketToolSessionRefs == nil {
		return
	}
	defaultWebsocketToolSessionRefs.acquire(sessionKey)
}

func releaseResponsesWebsocketToolCaches(sessionKey string) {
	if defaultWebsocketToolSessionRefs == nil {
		return
	}
	if !defaultWebsocketToolSessionRefs.release(sessionKey) {
		return
	}

	if defaultWebsocketToolOutputCache != nil {
		defaultWebsocketToolOutputCache.deleteSession(sessionKey)
	}
	if defaultWebsocketToolCallCache != nil {
		defaultWebsocketToolCallCache.deleteSession(sessionKey)
	}
}

func configureResponsesWebsocketToolCaches(sessionKey string, maxBytes int64) {
	perCacheBytes := responsesWebsocketPerCacheBudget(maxBytes)
	if defaultWebsocketToolOutputCache != nil {
		defaultWebsocketToolOutputCache.setSessionMaxBytes(sessionKey, perCacheBytes)
	}
	if defaultWebsocketToolCallCache != nil {
		defaultWebsocketToolCallCache.setSessionMaxBytes(sessionKey, perCacheBytes)
	}
}

func responsesWebsocketPerCacheBudget(maxBytes int64) int64 {
	perCacheBytes := maxBytes / 2
	if perCacheBytes <= 0 && maxBytes > 0 {
		return 1
	}
	return perCacheBytes
}

func clearResponsesWebsocketToolCaches(sessionKey string) {
	if defaultWebsocketToolOutputCache != nil {
		defaultWebsocketToolOutputCache.clearSessionItems(sessionKey)
	}
	if defaultWebsocketToolCallCache != nil {
		defaultWebsocketToolCallCache.clearSessionItems(sessionKey)
	}
}

func repairResponsesWebsocketToolCalls(sessionKey string, payload []byte) []byte {
	return repairResponsesWebsocketToolCallsWithCaches(defaultWebsocketToolOutputCache, defaultWebsocketToolCallCache, sessionKey, payload)
}

func sanitizeResponsesInputToolCallNames(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() || !input.IsArray() {
		return payload
	}

	sanitizedInput, errSanitize := sanitizeResponsesToolCallNamesArray(input.Raw)
	if errSanitize != nil || sanitizedInput == "" || sanitizedInput == input.Raw {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, "input", []byte(sanitizedInput))
	if errSet != nil {
		return payload
	}
	return updated
}

func sanitizeResponsesInputToolCallHistory(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() || !input.IsArray() {
		return payload
	}

	outputCache := newWebsocketToolOutputCache(time.Minute, len(input.Array())+1)
	callCache := newWebsocketToolOutputCache(time.Minute, len(input.Array())+1)
	sanitizedInput, errSanitize := repairResponsesToolCallsArray(outputCache, callCache, "sanitize-responses-input", input.Raw, false)
	if errSanitize != nil || sanitizedInput == "" || sanitizedInput == input.Raw {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, "input", []byte(sanitizedInput))
	if errSet != nil {
		return payload
	}
	return updated
}

func sanitizeResponsesToolCallNamesArray(rawArray string) (string, error) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return "[]", nil
	}

	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return "", errUnmarshal
	}

	validCallIDs := make(map[string]struct{}, len(items))
	invalidCallIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if !isResponsesToolCallType(itemType) {
			continue
		}
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		if callID == "" || !responsesToolCallHasValidName(item) {
			if callID != "" {
				invalidCallIDs[callID] = struct{}{}
			}
			continue
		}
		validCallIDs[callID] = struct{}{}
	}

	filtered := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		switch {
		case isResponsesToolCallType(itemType):
			if strings.TrimSpace(gjson.GetBytes(item, "call_id").String()) == "" || !responsesToolCallHasValidName(item) {
				continue
			}
			// Upstream requires function_call ids to begin with the "fc" prefix.
			// Some clients (e.g. Codex CLI) echo back function_call items whose id
			// mirrors the chat-completions "call_<hash>" format. Rewrite those
			// ids so the request is not rejected with
			// "Invalid 'input[N].id': 'call_...'. Expected an ID that begins with 'fc'".
			if itemType == "function_call" {
				if normalized, ok := normalizeResponsesFunctionCallItemID(item); ok {
					item = normalized
				}
			}
		case isResponsesToolCallOutputType(itemType):
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if _, invalid := invalidCallIDs[callID]; invalid {
				if _, valid := validCallIDs[callID]; !valid {
					continue
				}
			}
		}
		filtered = append(filtered, item)
	}

	out, errMarshal := json.Marshal(filtered)
	if errMarshal != nil {
		return "", errMarshal
	}
	return string(out), nil
}

// normalizeResponsesFunctionCallItemID rewrites the id field of a
// function_call input item so that it begins with the "fc" prefix required by
// the Responses API. Returns the updated item and true when the id was
// rewritten; returns the original item and false otherwise.
func normalizeResponsesFunctionCallItemID(item json.RawMessage) (json.RawMessage, bool) {
	if len(item) == 0 {
		return item, false
	}
	id := strings.TrimSpace(gjson.GetBytes(item, "id").String())
	if id == "" || strings.HasPrefix(id, "fc") {
		return item, false
	}
	normalized := "fc_" + strings.TrimPrefix(id, "call_")
	if normalized == id {
		return item, false
	}
	updated, errSet := sjson.SetBytes(item, "id", normalized)
	if errSet != nil {
		return item, false
	}
	return updated, true
}

func repairResponsesWebsocketToolCallsWithCache(cache *websocketToolOutputCache, sessionKey string, payload []byte) []byte {
	return repairResponsesWebsocketToolCallsWithCaches(cache, nil, sessionKey, payload)
}

func repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache *websocketToolOutputCache, sessionKey string, payload []byte) []byte {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || outputCache == nil || len(payload) == 0 {
		return payload
	}

	input := gjson.GetBytes(payload, "input")
	if !input.Exists() || !input.IsArray() {
		return payload
	}

	allowOrphanOutputs := strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != ""
	updatedRaw, errRepair := repairResponsesToolCallsArray(outputCache, callCache, sessionKey, input.Raw, allowOrphanOutputs)
	if errRepair != nil || updatedRaw == "" || updatedRaw == input.Raw {
		return payload
	}

	updated, errSet := sjson.SetRawBytes(payload, "input", []byte(updatedRaw))
	if errSet != nil {
		return payload
	}
	return updated
}

func repairResponsesToolCallsArray(outputCache, callCache *websocketToolOutputCache, sessionKey string, rawArray string, allowOrphanOutputs bool) (string, error) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return "[]", nil
	}

	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return "", errUnmarshal
	}

	// First pass: record tool outputs and remember which call_ids have outputs in this payload.
	outputPresent := make(map[string]struct{}, len(items))
	callPresent := make(map[string]struct{}, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		switch {
		case isResponsesToolCallOutputType(itemType):
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if callID == "" {
				continue
			}
			outputPresent[callID] = struct{}{}
			outputCache.record(sessionKey, callID, item)
		case isResponsesToolCallType(itemType):
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if callID == "" {
				continue
			}
			if !responsesToolCallHasValidName(item) {
				continue
			}
			callPresent[callID] = struct{}{}
			if callCache != nil {
				callCache.record(sessionKey, callID, item)
			}
		}
	}

	filtered := make([]json.RawMessage, 0, len(items))
	insertedCalls := make(map[string]struct{}, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if isResponsesToolCallOutputType(itemType) {
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if callID == "" {
				// Upstream rejects tool outputs without a call_id; drop it.
				continue
			}

			if _, ok := callPresent[callID]; ok {
				filtered = append(filtered, item)
				continue
			}

			if allowOrphanOutputs {
				filtered = append(filtered, item)
				continue
			}

			if callCache != nil {
				if cached, ok := callCache.get(sessionKey, callID); ok {
					if _, already := insertedCalls[callID]; !already {
						filtered = append(filtered, cached)
						insertedCalls[callID] = struct{}{}
						callPresent[callID] = struct{}{}
					}
					filtered = append(filtered, item)
					continue
				}
			}

			// Drop orphaned function_call_output items; upstream rejects transcripts with missing calls.
			continue
		}
		if !isResponsesToolCallType(itemType) {
			filtered = append(filtered, item)
			continue
		}

		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		if callID == "" {
			// Upstream rejects tool calls without a call_id; drop it.
			continue
		}
		if !responsesToolCallHasValidName(item) {
			// Upstream rejects tool calls without a non-empty name; drop it.
			continue
		}

		if _, ok := outputPresent[callID]; ok {
			filtered = append(filtered, item)
			continue
		}

		if allowOrphanOutputs {
			filtered = append(filtered, item)
			continue
		}

		if cached, ok := outputCache.get(sessionKey, callID); ok {
			filtered = append(filtered, item)
			filtered = append(filtered, cached)
			outputPresent[callID] = struct{}{}
			continue
		}

		// Drop orphaned function_call items; upstream rejects transcripts with missing outputs.
	}

	out, errMarshal := json.Marshal(filtered)
	if errMarshal != nil {
		return "", errMarshal
	}
	return string(out), nil
}

func recordResponsesWebsocketToolCallsFromPayload(sessionKey string, payload []byte) {
	recordResponsesWebsocketToolCallsFromPayloadWithCache(defaultWebsocketToolCallCache, sessionKey, payload)
}

func recordResponsesWebsocketToolCallsFromPayloadWithCache(cache *websocketToolOutputCache, sessionKey string, payload []byte) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || cache == nil || len(payload) == 0 {
		return
	}

	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	switch eventType {
	case "response.completed":
		output := gjson.GetBytes(payload, "response.output")
		if !output.Exists() || !output.IsArray() {
			return
		}
		for _, item := range output.Array() {
			if !isResponsesToolCallType(item.Get("type").String()) {
				continue
			}
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				continue
			}
			if !responsesToolCallHasValidName(json.RawMessage(item.Raw)) {
				continue
			}
			cache.record(sessionKey, callID, json.RawMessage(item.Raw))
		}
	case "response.output_item.added", "response.output_item.done":
		item := gjson.GetBytes(payload, "item")
		if !item.Exists() || !item.IsObject() {
			return
		}
		if !isResponsesToolCallType(item.Get("type").String()) {
			return
		}
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID == "" {
			return
		}
		if !responsesToolCallHasValidName(json.RawMessage(item.Raw)) {
			return
		}
		cache.record(sessionKey, callID, json.RawMessage(item.Raw))
	}
}

func responsesToolCallHasValidName(item json.RawMessage) bool {
	return strings.TrimSpace(gjson.GetBytes(item, "name").String()) != ""
}

func isResponsesToolCallType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

func isResponsesToolCallOutputType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "function_call_output", "custom_tool_call_output":
		return true
	default:
		return false
	}
}
