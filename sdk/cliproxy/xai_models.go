package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

const (
	xaiModelsPath               = "/models"
	xaiGrokBuildModel           = "grok-build"
	xaiGrokBuildUpstreamModel   = "grok-build-0.1"
	xaiModelFetchRequestTimeout = 15 * time.Second
)

var xaiModelCache = struct {
	mu     sync.RWMutex
	byAuth map[string][]*ModelInfo
}{byAuth: make(map[string][]*ModelInfo)}

type xaiModelsResponse struct {
	Data []xaiModelPayload `json:"data"`
}

type xaiModelPayload struct {
	ID                  string   `json:"id"`
	Aliases             []string `json:"aliases"`
	Object              string   `json:"object"`
	Created             int64    `json:"created"`
	OwnedBy             string   `json:"owned_by"`
	ContextLength       int      `json:"context_length"`
	MaxCompletionTokens int      `json:"max_completion_tokens"`
}

func (s *Service) fetchXAIModelsForAuth(ctx context.Context, auth *coreauth.Auth) []*ModelInfo {
	fallback := registry.GetXAIModels()
	cacheKey, token, baseURL := xaiModelFetchCredentials(auth)
	if token == "" {
		return fallback
	}
	if ctx == nil {
		ctx = context.Background()
	}
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), xaiModelFetchRequestTimeout)
	defer cancel()

	client := &http.Client{}
	if proxyURL := xaiModelFetchProxyURL(s, auth); proxyURL != "" {
		if transport, _, err := proxyutil.BuildHTTPTransport(proxyURL); err == nil && transport != nil {
			client.Transport = transport
		}
	}
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+xaiModelsPath, nil)
	if err != nil {
		return xaiCachedModelsOrFallback(cacheKey, fallback)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return xaiCachedModelsOrFallback(cacheKey, fallback)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, resp.Body)
		return xaiCachedModelsOrFallback(cacheKey, fallback)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return xaiCachedModelsOrFallback(cacheKey, fallback)
	}
	models := parseXAIModels(body, time.Now().Unix())
	if len(models) == 0 {
		return xaiCachedModelsOrFallback(cacheKey, fallback)
	}
	storeXAIModelAliases(auth, body)
	storeXAIModels(cacheKey, models)
	return models
}

func xaiModelFetchCredentials(auth *coreauth.Auth) (cacheKey, token, baseURL string) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "xai") {
		return "", "", ""
	}
	cacheKey = strings.TrimSpace(auth.ID)
	if auth.Metadata != nil {
		if value, ok := auth.Metadata["access_token"].(string); ok {
			token = strings.TrimSpace(value)
		}
		if value, ok := auth.Metadata["base_url"].(string); ok {
			baseURL = strings.TrimSpace(value)
		}
	}
	if auth.Attributes != nil {
		if token == "" {
			token = strings.TrimSpace(auth.Attributes["api_key"])
		}
		if baseURL == "" {
			baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		}
	}
	if baseURL == "" {
		baseURL = xaiauth.DefaultAPIBaseURL
	}
	return cacheKey, token, baseURL
}

func xaiModelFetchProxyURL(service *Service, auth *coreauth.Auth) string {
	if auth != nil && strings.TrimSpace(auth.ProxyURL) != "" {
		return strings.TrimSpace(auth.ProxyURL)
	}
	if service != nil && service.cfg != nil {
		return strings.TrimSpace(service.cfg.ProxyURL)
	}
	return ""
}

func xaiCachedModelsOrFallback(cacheKey string, fallback []*ModelInfo) []*ModelInfo {
	if cacheKey == "" {
		return fallback
	}
	xaiModelCache.mu.RLock()
	models := cloneXAIModels(xaiModelCache.byAuth[cacheKey])
	xaiModelCache.mu.RUnlock()
	if len(models) > 0 {
		return models
	}
	return fallback
}

func storeXAIModels(cacheKey string, models []*ModelInfo) {
	if cacheKey == "" || len(models) == 0 {
		return
	}
	xaiModelCache.mu.Lock()
	xaiModelCache.byAuth[cacheKey] = cloneXAIModels(models)
	xaiModelCache.mu.Unlock()
}

func cloneXAIModels(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	cloned := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		copy := *model
		if model.Thinking != nil {
			thinking := *model.Thinking
			thinking.Levels = append([]string(nil), model.Thinking.Levels...)
			copy.Thinking = &thinking
		}
		copy.SupportedParameters = append([]string(nil), model.SupportedParameters...)
		copy.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
		cloned = append(cloned, &copy)
	}
	return cloned
}

func parseXAIModels(body []byte, now int64) []*ModelInfo {
	var response xaiModelsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil
	}
	models := make([]*ModelInfo, 0, len(response.Data))
	seen := make(map[string]struct{}, len(response.Data))
	for _, item := range response.Data {
		modelID := strings.TrimSpace(item.ID)
		if modelID == "" {
			continue
		}
		model := xaiModelInfoFromPayload(item, modelID, now)
		addXAIModel(&models, seen, model)
		for _, alias := range item.Aliases {
			alias = strings.TrimSpace(alias)
			if alias == "" || strings.EqualFold(alias, modelID) {
				continue
			}
			aliasModel := *model
			aliasModel.ID = alias
			aliasModel.Name = alias
			addXAIModel(&models, seen, &aliasModel)
		}
	}
	return withXAIGrokBuildCompatibilityAlias(models)
}

func xaiModelInfoFromPayload(item xaiModelPayload, modelID string, now int64) *ModelInfo {
	model := &ModelInfo{
		ID:                  modelID,
		Object:              firstNonEmpty(item.Object, "model"),
		Created:             item.Created,
		OwnedBy:             firstNonEmpty(item.OwnedBy, "xai"),
		Type:                "xai",
		DisplayName:         modelID,
		Name:                modelID,
		Version:             modelID,
		ContextLength:       item.ContextLength,
		InputTokenLimit:     item.ContextLength,
		MaxCompletionTokens: item.MaxCompletionTokens,
		OutputTokenLimit:    item.MaxCompletionTokens,
	}
	if model.Created == 0 {
		model.Created = now
	}
	if static := registry.LookupStaticModelInfo(modelID); static != nil {
		model.Description = static.Description
		if strings.TrimSpace(static.DisplayName) != "" {
			model.DisplayName = static.DisplayName
		}
		if static.Thinking != nil {
			thinking := *static.Thinking
			thinking.Levels = append([]string(nil), static.Thinking.Levels...)
			model.Thinking = &thinking
		}
	}
	return model
}

func addXAIModel(models *[]*ModelInfo, seen map[string]struct{}, model *ModelInfo) {
	if model == nil || strings.TrimSpace(model.ID) == "" {
		return
	}
	key := strings.ToLower(strings.TrimSpace(model.ID))
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	*models = append(*models, model)
}

func withXAIGrokBuildCompatibilityAlias(models []*ModelInfo) []*ModelInfo {
	var upstream *ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		if strings.EqualFold(model.ID, xaiGrokBuildModel) {
			return models
		}
		if strings.EqualFold(model.ID, xaiGrokBuildUpstreamModel) {
			upstream = model
		}
	}
	if upstream == nil {
		return models
	}
	alias := *upstream
	alias.ID = xaiGrokBuildModel
	alias.Name = xaiGrokBuildModel
	alias.DisplayName = "Grok Build"
	return append(models, &alias)
}

func storeXAIModelAliases(auth *coreauth.Auth, body []byte) {
	if auth == nil {
		return
	}
	var response xaiModelsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return
	}
	aliases := coreauth.OAuthModelAliasesFromAttributes(auth.Attributes)
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		seen[strings.ToLower(strings.TrimSpace(alias.Alias))] = struct{}{}
	}
	for _, item := range response.Data {
		modelID := strings.TrimSpace(item.ID)
		if modelID == "" {
			continue
		}
		for _, alias := range item.Aliases {
			alias = strings.TrimSpace(alias)
			key := strings.ToLower(alias)
			if alias == "" || strings.EqualFold(alias, modelID) {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			aliases = append(aliases, sdkconfig.OAuthModelAlias{Name: modelID, Alias: alias})
		}
	}
	coreauth.SetOAuthModelAliasesAttribute(auth, aliases)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
