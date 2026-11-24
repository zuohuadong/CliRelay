// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// TokenResponse represents OAuth token response from Google
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// userInfo represents Google user profile
type userInfo struct {
	Email string `json:"email"`
}

// HTTPStatusError represents an HTTP error response with status code.
type HTTPStatusError struct {
	StatusCodeValue int
	Message         string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *HTTPStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCodeValue
}

// AntigravityAuth handles Antigravity OAuth authentication
type AntigravityAuth struct {
	httpClient *http.Client
	clientID   string
	secret     string
}

// NewAntigravityAuth creates a new Antigravity auth service.
func NewAntigravityAuth(cfg *config.Config, httpClient *http.Client) *AntigravityAuth {
	if cfg == nil {
		cfg = &config.Config{}
	}
	clientID, clientSecret := cfg.OAuthClientCredentials(config.OAuthClientAntigravity)
	if httpClient != nil {
		return &AntigravityAuth{httpClient: httpClient, clientID: clientID, secret: clientSecret}
	}
	return &AntigravityAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{}),
		clientID:   clientID,
		secret:     clientSecret,
	}
}

// NewAntigravityAuthWithProxy creates an auth client using a request-scoped proxy.
func NewAntigravityAuthWithProxy(cfg *config.Config, proxyURL string) *AntigravityAuth {
	if cfg == nil {
		cfg = &config.Config{}
	}
	clone := *cfg
	clone.SDKConfig = cfg.SDKConfig
	clone.ProxyURL = strings.TrimSpace(proxyURL)
	return NewAntigravityAuth(&clone, nil)
}

// RefreshAccessToken trades a refresh token for a fresh access token.
func (o *AntigravityAuth) RefreshAccessToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	clientID := strings.TrimSpace(o.clientID)
	if clientID == "" {
		clientID = ClientID
	}
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("client_secret", strings.TrimSpace(o.secret))
	if data.Get("client_secret") == "" {
		data.Set("client_secret", ClientSecret)
	}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", strings.TrimSpace(refreshToken))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("antigravity token refresh: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", APIUserAgent)
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("antigravity token refresh: execute request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, fmt.Errorf("antigravity token refresh: request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var token TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("antigravity token refresh: decode response: %w", err)
	}
	return &token, nil
}

func (o *AntigravityAuth) shortUserAgent() string {
	return misc.AntigravityRequestUserAgent("")
}

func (o *AntigravityAuth) nodeUserAgent() string {
	return misc.AntigravityOnboardUserUserAgent("")
}

func antigravityLoadCodeAssistMetadata() map[string]string {
	return map[string]string{
		"ideType": "ANTIGRAVITY",
	}
}

func antigravityControlPlaneMetadata(userAgent string) map[string]string {
	return map[string]string{
		"ide_type":    "ANTIGRAVITY",
		"ide_version": misc.AntigravityVersionFromUserAgent(userAgent),
		"ide_name":    "antigravity",
	}
}

func extractCloudaicompanionProject(data map[string]any) string {
	if data == nil {
		return ""
	}
	for _, key := range []string{"cloudaicompanionProject", "projectId", "project"} {
		switch value := data[key].(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case map[string]any:
			if id, ok := value["id"].(string); ok {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func defaultAntigravityTierID(loadResp map[string]any) string {
	if tiers, okTiers := loadResp["allowedTiers"].([]any); okTiers {
		for _, rawTier := range tiers {
			tier, okTier := rawTier.(map[string]any)
			if !okTier {
				continue
			}
			if isDefault, okDefault := tier["isDefault"].(bool); !okDefault || !isDefault {
				continue
			}
			if id, okID := tier["id"].(string); okID {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	if currentTier, okTier := loadResp["currentTier"].(map[string]any); okTier {
		if id, okID := currentTier["id"].(string); okID {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				return trimmed
			}
		}
	}
	return "free-tier"
}

// BuildAuthURL generates the OAuth authorization URL.
func (o *AntigravityAuth) BuildAuthURL(state, redirectURI string) string {
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = fmt.Sprintf("http://localhost:%d/oauth-callback", CallbackPort)
	}
	params := url.Values{}
	params.Set("access_type", "offline")
	params.Set("client_id", ClientID)
	params.Set("prompt", "consent")
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(Scopes, " "))
	params.Set("state", state)
	return AuthEndpoint + "?" + params.Encode()
}

// ExchangeCodeForTokens exchanges authorization code for access and refresh tokens
func (o *AntigravityAuth) ExchangeCodeForTokens(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("code", code)
	data.Set("client_id", ClientID)
	data.Set("client_secret", ClientSecret)
	data.Set("redirect_uri", redirectURI)
	data.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("antigravity token exchange: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("antigravity token exchange: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity token exchange: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if errRead != nil {
			return nil, fmt.Errorf("antigravity token exchange: read response: %w", errRead)
		}
		body := strings.TrimSpace(string(bodyBytes))
		msg := fmt.Sprintf("antigravity token exchange: request failed: status %d", resp.StatusCode)
		if body != "" {
			msg = fmt.Sprintf("antigravity token exchange: request failed: status %d: %s", resp.StatusCode, body)
		}
		return nil, &HTTPStatusError{StatusCodeValue: resp.StatusCode, Message: msg}
	}

	var token TokenResponse
	if errDecode := json.NewDecoder(resp.Body).Decode(&token); errDecode != nil {
		return nil, fmt.Errorf("antigravity token exchange: decode response: %w", errDecode)
	}
	return &token, nil
}

// FetchUserInfo retrieves user email from Google
func (o *AntigravityAuth) FetchUserInfo(ctx context.Context, accessToken string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", fmt.Errorf("antigravity userinfo: missing access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UserInfoEndpoint, nil)
	if err != nil {
		return "", fmt.Errorf("antigravity userinfo: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", o.shortUserAgent())

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return "", fmt.Errorf("antigravity userinfo: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity userinfo: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if errRead != nil {
			return "", fmt.Errorf("antigravity userinfo: read response: %w", errRead)
		}
		body := strings.TrimSpace(string(bodyBytes))
		msg := fmt.Sprintf("antigravity userinfo: request failed: status %d", resp.StatusCode)
		if body != "" {
			msg = fmt.Sprintf("antigravity userinfo: request failed: status %d: %s", resp.StatusCode, body)
		}
		return "", &HTTPStatusError{StatusCodeValue: resp.StatusCode, Message: msg}
	}
	var info userInfo
	if errDecode := json.NewDecoder(resp.Body).Decode(&info); errDecode != nil {
		return "", fmt.Errorf("antigravity userinfo: decode response: %w", errDecode)
	}
	email := strings.TrimSpace(info.Email)
	if email == "" {
		return "", fmt.Errorf("antigravity userinfo: response missing email")
	}
	return email, nil
}

// FetchAccountDetails retrieves project and tier information from loadCodeAssist.
func (o *AntigravityAuth) FetchAccountDetails(ctx context.Context, accessToken string) (AccountInfo, error) {
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]string{
			"ideType":    "ANTIGRAVITY",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	})
	if err != nil {
		return AccountInfo{}, fmt.Errorf("marshal request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/%s:loadCodeAssist", APIEndpoint, APIVersion),
		strings.NewReader(string(body)))
	if err != nil {
		return AccountInfo{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", o.shortUserAgent())
	req.Header.Set("X-Goog-Api-Client", misc.AntigravityGoogAPIClientUA)

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return AccountInfo{}, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return AccountInfo{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return AccountInfo{}, &HTTPStatusError{
			StatusCodeValue: resp.StatusCode,
			Message:         fmt.Sprintf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))),
		}
	}
	var loadResp map[string]any
	if err := json.Unmarshal(raw, &loadResp); err != nil {
		return AccountInfo{}, fmt.Errorf("decode response: %w", err)
	}

	info := AccountInfo{ProjectID: extractCloudaicompanionProject(loadResp)}
	if currentTier, ok := loadResp["currentTier"].(map[string]any); ok {
		info.TierID = stringFromMap(currentTier, "id")
		info.PlanType = stringFromMap(currentTier, "name", "id")
	}
	if paidTier, ok := loadResp["paidTier"].(map[string]any); ok && info.PlanType == "" {
		info.PlanType = stringFromMap(paidTier, "name", "id")
	}
	if info.ProjectID != "" {
		return info, nil
	}
	if err := unsupportedClientRefusal(loadResp); err != nil {
		return info, err
	}
	projectID, err := o.OnboardUser(ctx, accessToken, defaultAntigravityTierID(loadResp))
	if err != nil {
		if errors.Is(err, ErrNoProjectID) {
			if reasons := ineligibleTierReasons(loadResp); reasons != "" {
				err = fmt.Errorf("%w (ineligible tiers: %s)", err, reasons)
			}
		}
		return info, err
	}
	info.ProjectID = projectID
	return info, nil
}

func unsupportedClientRefusal(loadResp map[string]any) error {
	tiers, _ := loadResp["ineligibleTiers"].([]any)
	for _, raw := range tiers {
		tier, ok := raw.(map[string]any)
		if !ok || !strings.EqualFold(stringFromMap(tier, "reasonCode", "reason_code"), "UNSUPPORTED_CLIENT") {
			continue
		}
		if msg := stringFromMap(tier, "reasonMessage", "reason_message"); msg != "" {
			return fmt.Errorf("%w: %s", ErrUnsupportedClient, msg)
		}
		return ErrUnsupportedClient
	}
	return nil
}

func ineligibleTierReasons(loadResp map[string]any) string {
	tiers, _ := loadResp["ineligibleTiers"].([]any)
	var reasons []string
	for _, raw := range tiers {
		if tier, ok := raw.(map[string]any); ok {
			if code := stringFromMap(tier, "reasonCode", "reason_code"); code != "" {
				reasons = append(reasons, code)
			}
		}
	}
	return strings.Join(reasons, ", ")
}

func stringFromMap(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// FetchProjectID retrieves the project ID for the authenticated user via loadCodeAssist
func (o *AntigravityAuth) FetchProjectID(ctx context.Context, accessToken string) (string, error) {
	userAgent := o.shortUserAgent()
	loadReqBody := map[string]any{
		"metadata": antigravityLoadCodeAssistMetadata(),
	}

	rawBody, errMarshal := json.Marshal(loadReqBody)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal request body: %w", errMarshal)
	}

	endpointURL := fmt.Sprintf("%s/%s:loadCodeAssist", DailyAPIEndpoint, APIVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return "", fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity loadCodeAssist: close body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", fmt.Errorf("read response: %w", errRead)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", &HTTPStatusError{
			StatusCodeValue: resp.StatusCode,
			Message:         fmt.Sprintf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes))),
		}
	}

	var loadResp map[string]any
	if errDecode := json.Unmarshal(bodyBytes, &loadResp); errDecode != nil {
		return "", fmt.Errorf("decode response: %w", errDecode)
	}

	projectID := extractCloudaicompanionProject(loadResp)

	if projectID == "" {
		projectID, err = o.OnboardUser(ctx, accessToken, defaultAntigravityTierID(loadResp))
		if err != nil {
			return "", err
		}
		if projectID == "" {
			return "", fmt.Errorf("project id not found in loadCodeAssist or onboardUser response")
		}
		return projectID, nil
	}

	return projectID, nil
}

// OnboardUser attempts to fetch the project ID via onboardUser by polling for completion
func (o *AntigravityAuth) OnboardUser(ctx context.Context, accessToken, tierID string) (string, error) {
	log.Infof("Antigravity: onboarding user with tier: %s", tierID)
	userAgent := o.nodeUserAgent()
	requestBody := map[string]any{
		"tier_id":  tierID,
		"metadata": antigravityControlPlaneMetadata(userAgent),
	}

	rawBody, errMarshal := json.Marshal(requestBody)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal request body: %w", errMarshal)
	}

	maxAttempts := onboardMaxPolls
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		log.Debugf("Polling attempt %d/%d", attempt, maxAttempts)

		reqCtx := ctx
		var cancel context.CancelFunc
		if reqCtx == nil {
			reqCtx = context.Background()
		}
		reqCtx, cancel = context.WithTimeout(reqCtx, 30*time.Second)

		endpointURL := fmt.Sprintf("%s/%s:onboardUser", DailyAPIEndpoint, APIVersion)
		req, errRequest := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
		if errRequest != nil {
			cancel()
			return "", fmt.Errorf("create request: %w", errRequest)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("X-Goog-Api-Client", misc.AntigravityGoogAPIClientUA)

		resp, errDo := o.httpClient.Do(req)
		if errDo != nil {
			cancel()
			return "", fmt.Errorf("execute request: %w", errDo)
		}

		bodyBytes, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("close body error: %v", errClose)
		}
		cancel()

		if errRead != nil {
			return "", fmt.Errorf("read response: %w", errRead)
		}

		if resp.StatusCode == http.StatusOK {
			var data map[string]any
			if errDecode := json.Unmarshal(bodyBytes, &data); errDecode != nil {
				return "", fmt.Errorf("decode response: %w", errDecode)
			}

			if done, okDone := data["done"].(bool); okDone && done {
				projectID := ""
				if responseData, okResp := data["response"].(map[string]any); okResp {
					projectID = extractCloudaicompanionProject(responseData)
				}

				if projectID != "" {
					log.Infof("Successfully fetched project_id: %s", util.HideAPIKey(projectID))
					return projectID, nil
				}

				return "", ErrNoProjectID
			}

			if err := waitOnboardPoll(ctx); err != nil {
				return "", err
			}
			continue
		}

		responsePreview := strings.TrimSpace(string(bodyBytes))
		if len(responsePreview) > 500 {
			responsePreview = responsePreview[:500]
		}

		responseErr := responsePreview
		if len(responseErr) > 200 {
			responseErr = responseErr[:200]
		}
		return "", &HTTPStatusError{
			StatusCodeValue: resp.StatusCode,
			Message:         fmt.Sprintf("http %d: %s", resp.StatusCode, responseErr),
		}
	}

	return "", fmt.Errorf("%w after %d polls", ErrOnboardingPending, maxAttempts)
}

var (
	onboardMaxPolls     = 5
	onboardPollInterval = 2 * time.Second
)

func waitOnboardPoll(ctx context.Context) error {
	timer := time.NewTimer(onboardPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
