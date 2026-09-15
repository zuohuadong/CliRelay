package cliproxy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

const (
	antigravityModelBaseURLDaily      = "https://daily-cloudcode-pa.googleapis.com"
	antigravityModelBaseURLProd       = "https://cloudcode-pa.googleapis.com"
	antigravityModelsPath             = "/v1internal:fetchAvailableModels"
	antigravityCapabilityProbeTimeout = 5 * time.Second
	antigravityCapabilityCacheTTL     = 5 * time.Minute
	antigravityCapabilityFailureTTL   = 1 * time.Minute
)

type antigravityProbeStatus int

const (
	antigravityProbeStatusSuccess antigravityProbeStatus = iota
	antigravityProbeStatusAuthError
	antigravityProbeStatusTransientError
)

type antigravityCapabilityCacheEntry struct {
	hints     antigravityModelCapabilityHints
	expiresAt time.Time
}

type antigravityProbeResult struct {
	hints  antigravityModelCapabilityHints
	status antigravityProbeStatus
	token  string
}

var (
	antigravityNowFunc          = time.Now
	antigravityCapabilityMu     sync.RWMutex
	antigravityCapabilityCache  = make(map[string]antigravityCapabilityCacheEntry)
	antigravityAuthFailureCache = make(map[string]time.Time)
	antigravityCapabilityGroup  singleflight.Group
	antigravityProbeInFlight    atomic.Int32
)

func purgeExpiredAntigravityCacheLocked(now time.Time) {
	for k, v := range antigravityCapabilityCache {
		if !now.Before(v.expiresAt) {
			delete(antigravityCapabilityCache, k)
		}
	}
	for k, exp := range antigravityAuthFailureCache {
		if !now.Before(exp) {
			delete(antigravityAuthFailureCache, k)
		}
	}
}

type antigravityFetchAvailableModelsResponse struct {
	WebSearchModelIDs []string `json:"webSearchModelIds"`
	Models            map[string]struct {
		DisplayName     string `json:"displayName"`
		MaxTokens       int64  `json:"maxTokens"`
		MaxOutputTokens int64  `json:"maxOutputTokens"`
		IsInternal      bool   `json:"isInternal"`
	} `json:"models"`
}

type antigravityModelCapabilityHints struct {
	WebSearchModelIDs map[string]struct{}
}

type antigravityFetchedModelsResult struct {
	Models []*ModelInfo
	Hints  antigravityModelCapabilityHints
}

func (h antigravityModelCapabilityHints) clone() antigravityModelCapabilityHints {
	if h.WebSearchModelIDs == nil {
		return antigravityModelCapabilityHints{}
	}
	cloned := make(map[string]struct{}, len(h.WebSearchModelIDs))
	for k := range h.WebSearchModelIDs {
		cloned[k] = struct{}{}
	}
	return antigravityModelCapabilityHints{WebSearchModelIDs: cloned}
}

func (s *Service) fetchAntigravityModelCapabilityHintsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityModelCapabilityHints {
	if auth == nil || auth.Metadata == nil {
		return antigravityModelCapabilityHints{}
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return antigravityModelCapabilityHints{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	baseURLs := antigravityModelBaseURLs(auth)
	if len(baseURLs) == 0 {
		return antigravityModelCapabilityHints{}
	}
	proxyURL := s.antigravityModelFetchProxyURL(auth)
	cacheKey := strings.Join(baseURLs, "|") + "#" + proxyURL
	tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(accessToken)))
	authFailKey := auth.ID + "#" + tokenHash
	now := antigravityNowFunc()
	antigravityCapabilityMu.RLock()
	if failExpiry, failed := antigravityAuthFailureCache[authFailKey]; failed && now.Before(failExpiry) {
		antigravityCapabilityMu.RUnlock()
		return antigravityModelCapabilityHints{}
	}
	if entry, ok := antigravityCapabilityCache[cacheKey]; ok && now.Before(entry.expiresAt) {
		hints := entry.hints.clone()
		antigravityCapabilityMu.RUnlock()
		return hints
	}
	antigravityCapabilityMu.RUnlock()
	antigravityProbeInFlight.Add(1)
	defer antigravityProbeInFlight.Add(-1)
	res, errDo, _ := antigravityCapabilityGroup.Do(cacheKey, func() (any, error) {
		nowInside := antigravityNowFunc()
		antigravityCapabilityMu.RLock()
		if entry, ok := antigravityCapabilityCache[cacheKey]; ok && nowInside.Before(entry.expiresAt) {
			hints := entry.hints.clone()
			antigravityCapabilityMu.RUnlock()
			return antigravityProbeResult{hints: hints, status: antigravityProbeStatusSuccess, token: accessToken}, nil
		}
		antigravityCapabilityMu.RUnlock()
		hints, status := s.probeAntigravityModelCapabilityHints(ctx, auth, baseURLs, proxyURL, accessToken)
		if status == antigravityProbeStatusSuccess || status == antigravityProbeStatusTransientError {
			if status != antigravityProbeStatusTransientError || ctx.Err() == nil {
				ttl := antigravityCapabilityCacheTTL
				if status != antigravityProbeStatusSuccess {
					ttl = antigravityCapabilityFailureTTL
				}
				antigravityCapabilityMu.Lock()
				purgeExpiredAntigravityCacheLocked(nowInside)
				antigravityCapabilityCache[cacheKey] = antigravityCapabilityCacheEntry{hints: hints.clone(), expiresAt: antigravityNowFunc().Add(ttl)}
				antigravityCapabilityMu.Unlock()
			}
		}
		return antigravityProbeResult{hints: hints, status: status, token: accessToken}, nil
	})
	if errDo != nil || res == nil {
		return antigravityModelCapabilityHints{}
	}
	result, ok := res.(antigravityProbeResult)
	if !ok {
		return antigravityModelCapabilityHints{}
	}
	if result.status == antigravityProbeStatusAuthError {
		antigravityCapabilityMu.Lock()
		purgeExpiredAntigravityCacheLocked(now)
		antigravityAuthFailureCache[authFailKey] = antigravityNowFunc().Add(antigravityCapabilityFailureTTL)
		antigravityCapabilityMu.Unlock()
		return antigravityModelCapabilityHints{}
	}
	return result.hints.clone()
}

func (s *Service) fetchAntigravityModelsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityFetchedModelsResult {
	if auth == nil || auth.Metadata == nil {
		return antigravityFetchedModelsResult{}
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return antigravityFetchedModelsResult{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	baseURLs := antigravityModelBaseURLs(auth)
	if len(baseURLs) == 0 {
		return antigravityFetchedModelsResult{}
	}
	proxyURL := s.antigravityModelFetchProxyURL(auth)
	client := &http.Client{}
	if transport, _, errProxy := proxyutil.BuildHTTPTransport(proxyURL); errProxy == nil && transport != nil {
		client.Transport = transport
	}
	payload := antigravityModelsRequestPayload(auth)
	for _, baseURL := range baseURLs {
		req, errReq := http.NewRequestWithContext(fetchCtx, http.MethodPost, strings.TrimRight(baseURL, "/")+antigravityModelsPath, strings.NewReader(payload))
		if errReq != nil {
			continue
		}
		req.Close = true
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("User-Agent", misc.AntigravityUserAgent())
		resp, errDo := client.Do(req)
		if errDo != nil {
			continue
		}
		body, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("antigravity model fetch: close response body: %v", errClose)
		}
		if errRead != nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			continue
		}
		result := parseAntigravityFetchedModels(body)
		if len(result.Models) > 0 || len(result.Hints.WebSearchModelIDs) > 0 {
			return result
		}
	}
	return antigravityFetchedModelsResult{}
}

func (s *Service) probeAntigravityModelCapabilityHints(ctx context.Context, auth *coreauth.Auth, baseURLs []string, proxyURL string, accessToken string) (antigravityModelCapabilityHints, antigravityProbeStatus) {
	probeCtx := context.Background()
	var cancel context.CancelFunc
	probeCtx, cancel = context.WithTimeout(probeCtx, antigravityCapabilityProbeTimeout)
	defer cancel()

	client := &http.Client{
		Timeout: antigravityCapabilityProbeTimeout,
	}
	if transport, _, errProxy := proxyutil.BuildHTTPTransport(proxyURL); errProxy == nil && transport != nil {
		client.Transport = transport
	}

	if len(baseURLs) == 1 {
		return s.fetchAntigravityModelHintsFromURL(probeCtx, client, baseURLs[0], accessToken)
	}

	type probeResult struct {
		hints  antigravityModelCapabilityHints
		status antigravityProbeStatus
	}
	ch := make(chan probeResult, len(baseURLs))
	for _, baseURL := range baseURLs {
		go func(url string) {
			h, status := s.fetchAntigravityModelHintsFromURL(probeCtx, client, url, accessToken)
			ch <- probeResult{hints: h, status: status}
		}(baseURL)
	}

	var firstSuccess antigravityModelCapabilityHints
	var hadSuccess bool
	overallStatus := antigravityProbeStatusTransientError
	for i := 0; i < len(baseURLs); i++ {
		select {
		case <-probeCtx.Done():
			return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
		case res := <-ch:
			if res.status == antigravityProbeStatusSuccess {
				if len(res.hints.WebSearchModelIDs) > 0 {
					return res.hints, antigravityProbeStatusSuccess
				}
				if !hadSuccess {
					firstSuccess = res.hints
					hadSuccess = true
				}
			} else if res.status == antigravityProbeStatusAuthError {
				overallStatus = antigravityProbeStatusAuthError
			}
		}
	}
	if hadSuccess {
		return firstSuccess, antigravityProbeStatusSuccess
	}
	return antigravityModelCapabilityHints{}, overallStatus
}

func (s *Service) fetchAntigravityModelHintsFromURL(ctx context.Context, client *http.Client, baseURL string, accessToken string) (antigravityModelCapabilityHints, antigravityProbeStatus) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+antigravityModelsPath, strings.NewReader(`{}`))
	if errReq != nil {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	req.Close = true
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", misc.AntigravityUserAgent())

	resp, errDo := client.Do(req)
	if errDo != nil {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("antigravity model fetch: close response body: %v", errClose)
		}
	}()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusAuthError
	}
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	hints, ok := parseAntigravityModelCapabilityHints(body)
	if !ok {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	return hints, antigravityProbeStatusSuccess
}

func (s *Service) antigravityModelFetchProxyURL(auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s != nil {
		s.cfgMu.RLock()
		defer s.cfgMu.RUnlock()
		if s.cfg != nil {
			return strings.TrimSpace(s.cfg.ProxyURL)
		}
	}
	return ""
}

func antigravityModelBaseURLs(auth *coreauth.Auth) []string {
	if auth != nil && auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["base_urls"]); raw != "" {
			parts := strings.Split(raw, ",")
			urls := make([]string, 0, len(parts))
			for _, p := range parts {
				if trimmed := strings.TrimRight(strings.TrimSpace(p), "/"); trimmed != "" {
					urls = append(urls, trimmed)
				}
			}
			if len(urls) > 0 {
				return urls
			}
		}
	}
	if baseURL := resolveAntigravityModelBaseURL(auth); baseURL != "" {
		return []string{baseURL}
	}
	return []string{antigravityModelBaseURLDaily}
}

func resolveAntigravityModelBaseURL(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata["base_url"].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return strings.TrimRight(value, "/")
			}
		}
	}
	return ""
}

func antigravityModelsRequestPayload(auth *coreauth.Auth) string {
	projectID := ""
	if auth != nil {
		if auth.Metadata != nil {
			projectID = antigravityMetadataString(auth.Metadata, "project_id")
			if projectID == "" {
				projectID = antigravityMetadataString(auth.Metadata, "project")
			}
		}
		if projectID == "" && auth.Attributes != nil {
			projectID = strings.TrimSpace(auth.Attributes["project_id"])
			if projectID == "" {
				projectID = strings.TrimSpace(auth.Attributes["project"])
			}
		}
	}
	if projectID == "" {
		return `{}`
	}
	payload, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return `{}`
	}
	return string(payload)
}

func antigravityMetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func parseAntigravityFetchedModels(body []byte) antigravityFetchedModelsResult {
	var parsed antigravityFetchAvailableModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return antigravityFetchedModelsResult{}
	}
	hints := antigravityWebSearchHints(parsed.WebSearchModelIDs)
	models := antigravityModelInfosFromFetched(parsed.Models, hints)
	return antigravityFetchedModelsResult{Models: models, Hints: hints}
}

func parseAntigravityModelCapabilityHints(body []byte) (antigravityModelCapabilityHints, bool) {
	var parsed antigravityFetchAvailableModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return antigravityModelCapabilityHints{}, false
	}
	return antigravityWebSearchHints(parsed.WebSearchModelIDs), true
}

func antigravityWebSearchHints(ids []string) antigravityModelCapabilityHints {
	webSearchModels := make(map[string]struct{}, len(ids))
	for _, modelID := range ids {
		modelID = normalizeAntigravityFetchedModelID(modelID)
		if modelID != "" {
			webSearchModels[modelID] = struct{}{}
		}
	}
	return antigravityModelCapabilityHints{WebSearchModelIDs: webSearchModels}
}

func antigravityModelInfosFromFetched(models map[string]struct {
	DisplayName     string `json:"displayName"`
	MaxTokens       int64  `json:"maxTokens"`
	MaxOutputTokens int64  `json:"maxOutputTokens"`
	IsInternal      bool   `json:"isInternal"`
}, hints antigravityModelCapabilityHints) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	now := time.Now().Unix()
	out := make([]*ModelInfo, 0, len(models))
	for rawID, data := range models {
		modelID := strings.TrimSpace(rawID)
		if modelID == "" || isInternalAntigravityFetchedModel(modelID, data.IsInternal) {
			continue
		}
		displayName := strings.TrimSpace(data.DisplayName)
		if displayName == "" {
			displayName = modelID
		}
		model := &ModelInfo{
			ID:          modelID,
			Object:      "model",
			Created:     now,
			OwnedBy:     "antigravity",
			Type:        "antigravity",
			DisplayName: displayName,
			Name:        modelID,
			Version:     modelID,
			Description: displayName,
			Thinking:    antigravityFetchedModelThinking(modelID),
		}
		if data.MaxTokens > 0 {
			model.ContextLength = int(data.MaxTokens)
			model.InputTokenLimit = int(data.MaxTokens)
		}
		if data.MaxOutputTokens > 0 {
			model.MaxCompletionTokens = int(data.MaxOutputTokens)
			model.OutputTokenLimit = int(data.MaxOutputTokens)
		}
		if _, ok := hints.WebSearchModelIDs[normalizeAntigravityFetchedModelID(modelID)]; ok {
			model.SupportsWebSearch = true
		}
		out = append(out, model)
	}
	return out
}

func isInternalAntigravityFetchedModel(modelID string, isInternal bool) bool {
	id := normalizeAntigravityFetchedModelID(modelID)
	if id == "" || isInternal {
		return true
	}
	return strings.HasPrefix(id, "chat_") ||
		strings.HasPrefix(id, "tab_") ||
		strings.HasPrefix(id, "tab-jump") ||
		strings.HasPrefix(id, "tab_jump")
}

func antigravityFetchedModelThinking(modelID string) *registry.ThinkingSupport {
	switch normalizeAntigravityFetchedModelID(modelID) {
	case "gemini-3-pro-high", "gemini-3-pro-low", "gemini-3-pro-image", "gemini-3.1-pro-high", "gemini-3.1-pro-low":
		return &registry.ThinkingSupport{Min: 128, Max: 32768, ZeroAllowed: false, DynamicAllowed: true, Levels: []string{"low", "high"}}
	case "gemini-3-flash", "gemini-3-flash-agent", "gemini-3.1-flash-lite", "gemini-3.5-flash-low", "gemini-3.5-flash-extra-low":
		return &registry.ThinkingSupport{Min: 128, Max: 32768, ZeroAllowed: false, DynamicAllowed: true, Levels: []string{"minimal", "low", "medium", "high"}}
	case "claude-sonnet-4-5-thinking", "claude-sonnet-4-6-thinking", "claude-opus-4-5-thinking", "claude-opus-4-6-thinking", "claude-opus-4-7-thinking", "claude-opus-4-8-thinking":
		return &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true, DynamicAllowed: true}
	default:
		return nil
	}
}

func applyAntigravityFetchedModelCapabilities(models []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	if len(models) == 0 || len(hints.WebSearchModelIDs) == 0 {
		return models
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := normalizeAntigravityFetchedModelID(model.ID)
		if _, ok := hints.WebSearchModelIDs[modelID]; ok {
			model.SupportsWebSearch = true
		}
	}
	return models
}

func mergeAntigravityFetchedModels(staticModels, fetchedModels []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	if len(staticModels) == 0 {
		return filterRegisterableAntigravityFetchedModels(fetchedModels)
	}
	models := applyAntigravityFetchedModelCapabilities(staticModels, hints)
	staticIDs := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if id := normalizeAntigravityFetchedModelID(model.ID); id != "" {
			staticIDs[id] = struct{}{}
		}
	}
	for _, model := range fetchedModels {
		if model == nil {
			continue
		}
		modelID := normalizeAntigravityFetchedModelID(model.ID)
		if modelID == "" {
			continue
		}
		if _, exists := staticIDs[modelID]; exists {
			continue
		}
		if !isRegisterableAntigravityFetchedModel(modelID) {
			continue
		}
		models = append(models, model)
		staticIDs[modelID] = struct{}{}
	}
	return models
}

func filterRegisterableAntigravityFetchedModels(models []*ModelInfo) []*ModelInfo {
	out := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if isRegisterableAntigravityFetchedModel(normalizeAntigravityFetchedModelID(model.ID)) {
			out = append(out, model)
		}
	}
	return out
}

func isRegisterableAntigravityFetchedModel(modelID string) bool {
	switch normalizeAntigravityFetchedModelID(modelID) {
	case "gemini-3.1-pro-high":
		return true
	default:
		return false
	}
}

func normalizeAntigravityFetchedModelID(modelID string) string {
	return strings.ToLower(strings.TrimSpace(modelID))
}

// WaitAntigravityProbes waits for any in-flight asynchronous Antigravity capability probes to complete.
func (s *Service) WaitAntigravityProbes() {
	if s == nil {
		return
	}
	s.antigravityProbeWg.Wait()
}

func (s *Service) waitAntigravityProbesContext(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		s.antigravityProbeWg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
	case <-done:
	}
}

func (s *Service) asyncProbeAntigravityCapabilities(ctx context.Context, auth *coreauth.Auth, providerKey string) {
	if auth == nil || auth.ID == "" || auth.Disabled {
		return
	}
	authClone := auth.Clone()
	expectedEpoch := auth.RegistrationEpoch
	expectedPrefix := auth.Prefix
	expectedRegEpoch := GlobalModelRegistry().ClientRegistrationEpoch(auth.ID)

	probeCtx := ctx
	if probeCtx == nil {
		probeCtx = context.Background()
	}

	if s != nil {
		s.antigravityProbeWg.Add(1)
	}

	go func() {
		if s != nil {
			defer s.antigravityProbeWg.Done()
		}
		hints := s.fetchAntigravityModelCapabilityHintsForAuth(probeCtx, authClone)
		if len(hints.WebSearchModelIDs) == 0 {
			return
		}
		if s == nil {
			return
		}
		if s.coreManager != nil {
			current, exists := s.coreManager.GetByID(authClone.ID)
			if !exists || current == nil || current.Disabled {
				return
			}
			// Version protection: verify auth registration epoch and prefix did not change.
			// Auth.Generation is intentionally NOT checked here because regular request completions
			// increment Generation on every request (via MarkResult), which must not invalidate valid probe results.
			if current.RegistrationEpoch != expectedEpoch || current.Prefix != expectedPrefix {
				return
			}
		}
		aliasMap := s.buildAntigravityReverseAliasMap(authClone)

		// Atomically update capabilities on existing registered models if epoch matches
		updated := GlobalModelRegistry().ApplyClientModelCapabilities(authClone.ID, expectedRegEpoch, func(modelID string, info *ModelInfo) {
			upstreamID := resolveAntigravityUpstreamModelID(modelID, authClone.Prefix, aliasMap)
			if _, ok := hints.WebSearchModelIDs[upstreamID]; ok {
				info.SupportsWebSearch = true
			}
		})
		if !updated {
			return
		}

		if s.coreManager != nil {
			s.coreManager.ReconcileRegistryModelStates(context.Background(), authClone.ID)
			s.coreManager.RefreshSchedulerEntry(authClone.ID)
		}
	}()
}

func (s *Service) buildAntigravityReverseAliasMap(auth *coreauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	var cfg *config.Config
	if s != nil {
		s.cfgMu.RLock()
		cfg = s.cfg
		s.cfgMu.RUnlock()
	}
	channel := coreauth.OAuthModelAliasChannel(auth.Provider, auth.AuthKind())
	aliases := oauthModelAliasesForAuth(cfg, channel, auth.Attributes)
	if len(aliases) == 0 {
		return nil
	}
	aliasMap := make(map[string]string, len(aliases))
	for _, entry := range aliases {
		aliasName := strings.ToLower(strings.TrimSpace(entry.Alias))
		upstreamName := strings.ToLower(strings.TrimSpace(entry.Name))
		if aliasName != "" && upstreamName != "" {
			aliasMap[aliasName] = upstreamName
		}
	}
	return aliasMap
}

func resolveAntigravityUpstreamModelID(modelID string, prefix string, aliasMap map[string]string) string {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	prefix = strings.ToLower(strings.Trim(strings.TrimSpace(prefix), "/"))
	unprefixed := modelID
	if prefix != "" && strings.HasPrefix(modelID, prefix+"/") {
		unprefixed = modelID[len(prefix)+1:]
	}
	if len(aliasMap) > 0 {
		if upstream, ok := aliasMap[unprefixed]; ok && upstream != "" {
			return upstream
		}
	}
	return unprefixed
}
