package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	antigravityModelBaseURLDaily = "https://daily-cloudcode-pa.googleapis.com"
	antigravityModelBaseURLProd  = "https://cloudcode-pa.googleapis.com"
	antigravityModelsPath        = "/v1internal:fetchAvailableModels"
)

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

func (s *Service) fetchAntigravityModelCapabilityHintsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityModelCapabilityHints {
	return s.fetchAntigravityModelsForAuth(ctx, auth).Hints
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
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	client := &http.Client{}
	if transport, _, errProxy := proxyutil.BuildHTTPTransport(s.antigravityModelFetchProxyURL(auth)); errProxy == nil && transport != nil {
		client.Transport = transport
	}

	payload := antigravityModelsRequestPayload(auth)
	for _, baseURL := range antigravityModelBaseURLs(auth) {
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
		if errRead != nil {
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			continue
		}
		result := parseAntigravityFetchedModels(body)
		if len(result.Models) > 0 || len(result.Hints.WebSearchModelIDs) > 0 {
			return result
		}
	}
	return antigravityFetchedModelsResult{}
}

func (s *Service) antigravityModelFetchProxyURL(auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s != nil && s.cfg != nil {
		return strings.TrimSpace(s.cfg.ProxyURL)
	}
	return ""
}

func antigravityModelBaseURLs(auth *coreauth.Auth) []string {
	if baseURL := resolveAntigravityModelBaseURL(auth); baseURL != "" {
		return []string{baseURL}
	}
	return []string{antigravityModelBaseURLDaily, antigravityModelBaseURLProd}
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

func parseAntigravityModelCapabilityHints(body []byte) antigravityModelCapabilityHints {
	var parsed antigravityFetchAvailableModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return antigravityModelCapabilityHints{}
	}
	return antigravityWebSearchHints(parsed.WebSearchModelIDs)
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

func normalizeAntigravityFetchedModelID(modelID string) string {
	return strings.ToLower(strings.TrimSpace(modelID))
}
