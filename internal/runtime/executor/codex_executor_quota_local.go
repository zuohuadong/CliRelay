package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	codexResetCreditsURL        = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	codexResetCreditsConsumeURL = codexResetCreditsURL + "/consume"
	codexResetCreditsTimeout    = time.Minute
)

// ProbeQuotaRecovery performs a lightweight quota check for Codex auths by calling
// the ChatGPT usage endpoint. It implements the cliproxyauth.QuotaRecoveryProber interface.
func (e *CodexExecutor) ProbeQuotaRecovery(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.QuotaProbeResult, error) {
	var err error
	auth, err = e.resolveEgressAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, fmt.Errorf("codex executor: auth is nil")
	}
	accountID := codexAccountIDFromAuth(auth)
	if accountID == "" {
		return nil, fmt.Errorf("codex executor: missing account_id")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", nil)
	if err != nil {
		return nil, err
	}
	apiKey, _ := codexCreds(auth)
	if err = applyCodexHeaders(req, auth, apiKey, false, e.cfg); err != nil {
		return nil, err
	}

	httpClient, err := e.outboundHTTPClient(ctx, auth, 0, 0, false)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close quota probe body error: %v", errClose)
		}
	}()

	body, err := readUpstreamResponseBody(e.Identifier(), resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, newCodexStatusErrForResponse(resp, body)
	}
	return parseCodexQuotaProbe(body), nil
}

// CodexRateLimitResetCredit is one earned, account-scoped usage reset credit.
type CodexRateLimitResetCredit struct {
	ID          string  `json:"id"`
	ResetType   string  `json:"reset_type"`
	Status      string  `json:"status"`
	GrantedAt   string  `json:"granted_at"`
	ExpiresAt   *string `json:"expires_at"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

// CodexRateLimitResetCredits is the account's reset-credit inventory.
type CodexRateLimitResetCredits struct {
	Credits        []CodexRateLimitResetCredit `json:"credits"`
	AvailableCount int64                       `json:"available_count"`
}

// CodexRateLimitResetResult reports the outcome of one idempotent redemption.
type CodexRateLimitResetResult struct {
	Code         string `json:"code"`
	WindowsReset int64  `json:"windows_reset"`
}

type codexRateLimitResetRequest struct {
	RedeemRequestID string `json:"redeem_request_id"`
	CreditID        string `json:"credit_id"`
}

// ListRateLimitResetCredits returns the reset credits currently attached to a Codex account.
func (e *CodexExecutor) ListRateLimitResetCredits(ctx context.Context, auth *cliproxyauth.Auth) (*CodexRateLimitResetCredits, error) {
	body, err := e.codexResetCreditsRequest(ctx, auth, http.MethodGet, codexResetCreditsURL, nil)
	if err != nil {
		return nil, err
	}
	return decodeCodexRateLimitResetCredits(body)
}

// ConsumeRateLimitResetCredit redeems one selected credit using an idempotent request ID.
func (e *CodexExecutor) ConsumeRateLimitResetCredit(ctx context.Context, auth *cliproxyauth.Auth, creditID, redeemRequestID string) (*CodexRateLimitResetResult, error) {
	payload, err := json.Marshal(codexRateLimitResetRequest{
		RedeemRequestID: redeemRequestID,
		CreditID:        creditID,
	})
	if err != nil {
		return nil, fmt.Errorf("codex reset credits: encode consume request: %w", err)
	}
	body, err := e.codexResetCreditsRequest(ctx, auth, http.MethodPost, codexResetCreditsConsumeURL, payload)
	if err != nil {
		return nil, err
	}
	return decodeCodexRateLimitResetResult(body)
}

func decodeCodexRateLimitResetCredits(body []byte) (*CodexRateLimitResetCredits, error) {
	var response struct {
		Credits        *[]CodexRateLimitResetCredit `json:"credits"`
		AvailableCount *int64                       `json:"available_count"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("codex reset credits: decode list response: %w", err)
	}
	if response.Credits == nil || response.AvailableCount == nil || *response.AvailableCount < 0 {
		return nil, errors.New("codex reset credits: invalid list response")
	}
	for _, credit := range *response.Credits {
		if !validCodexRateLimitResetCredit(credit) {
			return nil, errors.New("codex reset credits: invalid credit entry")
		}
	}
	return &CodexRateLimitResetCredits{Credits: *response.Credits, AvailableCount: *response.AvailableCount}, nil
}

func validCodexRateLimitResetCredit(credit CodexRateLimitResetCredit) bool {
	return strings.TrimSpace(credit.ID) != "" &&
		strings.TrimSpace(credit.ResetType) != "" &&
		strings.TrimSpace(credit.Status) != "" &&
		strings.TrimSpace(credit.GrantedAt) != ""
}

func decodeCodexRateLimitResetResult(body []byte) (*CodexRateLimitResetResult, error) {
	var response struct {
		Code         *string `json:"code"`
		WindowsReset *int64  `json:"windows_reset"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("codex reset credits: decode consume response: %w", err)
	}
	if response.Code == nil || response.WindowsReset == nil || *response.WindowsReset < 0 {
		return nil, errors.New("codex reset credits: invalid consume response")
	}
	code := strings.ToLower(strings.TrimSpace(*response.Code))
	if code != "reset" && code != "nothing_to_reset" && code != "no_credit" && code != "already_redeemed" {
		return nil, errors.New("codex reset credits: unknown consume outcome")
	}
	return &CodexRateLimitResetResult{Code: code, WindowsReset: *response.WindowsReset}, nil
}

func (e *CodexExecutor) codexResetCreditsRequest(ctx context.Context, auth *cliproxyauth.Auth, method, targetURL string, payload []byte) ([]byte, error) {
	resolvedAuth, err := e.resolveEgressAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	if resolvedAuth == nil || !strings.EqualFold(strings.TrimSpace(resolvedAuth.Provider), "codex") {
		return nil, statusErr{code: http.StatusBadRequest, msg: "codex reset credits require Codex OAuth auth"}
	}
	if resolvedAuth.AuthKind() != cliproxyauth.AuthKindOAuth {
		return nil, statusErr{code: http.StatusBadRequest, msg: "codex reset credits require Codex OAuth auth"}
	}
	token, _ := resolvedAuth.Metadata["access_token"].(string)
	if strings.TrimSpace(token) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "codex reset credits require an access token"}
	}
	accountID := codexAccountIDFromAuth(resolvedAuth)
	if accountID == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "codex reset credits require a ChatGPT account ID"}
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if err = applyCodexHeaders(req, resolvedAuth, token, false, e.cfg); err != nil {
		return nil, err
	}
	applyCodexResetCreditSecurityHeaders(req, token, accountID)
	client, err := e.outboundHTTPClient(ctx, resolvedAuth, codexResetCreditsTimeout, 0, false)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, e.wrapStrictEgressTransportErrorForAuth(resolvedAuth, err, "reset credits request")
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Errorf("codex executor: close reset-credit body error: %v", closeErr)
		}
	}()
	body, err := readUpstreamResponseBody(e.Identifier(), resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, newCodexStatusErrForResponse(resp, body)
	}
	return body, nil
}

func applyCodexResetCreditSecurityHeaders(req *http.Request, token, accountID string) {
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Chatgpt-Account-Id", strings.TrimSpace(accountID))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Host = req.URL.Host
	req.Header.Del("Host")
}

func parseCodexQuotaProbe(body []byte) *cliproxyauth.QuotaProbeResult {
	if len(body) == 0 {
		return nil
	}

	rateLimit := gjson.GetBytes(body, "rate_limit")
	if !rateLimit.Exists() {
		return nil
	}

	allowed := rateLimit.Get("allowed")
	limitReached := rateLimit.Get("limit_reached")
	if limitReached.Exists() && limitReached.Bool() {
		return &cliproxyauth.QuotaProbeResult{
			Recovered:     false,
			NextRecoverAt: codexQuotaProbeNextRecoverAt(rateLimit, false),
		}
	}

	hasWindowUsage := false
	hasExhaustedWindow := false
	nextRecoverAt := time.Time{}
	for _, path := range []string{"primary_window", "secondary_window"} {
		window := rateLimit.Get(path)
		if !window.Exists() {
			continue
		}
		usedPercent := window.Get("used_percent")
		windowExhausted := false
		if usedPercent.Exists() {
			hasWindowUsage = true
			windowExhausted = usedPercent.Float() >= 100
			if windowExhausted {
				hasExhaustedWindow = true
			}
		}
		if !windowExhausted {
			continue
		}
		if resetAt := codexQuotaWindowResetAt(window, time.Now()); !resetAt.IsZero() {
			if nextRecoverAt.IsZero() || resetAt.Before(nextRecoverAt) {
				nextRecoverAt = resetAt
			}
		}
	}

	if !hasExhaustedWindow {
		if allowed.Exists() {
			return &cliproxyauth.QuotaProbeResult{
				Recovered:     allowed.Bool(),
				NextRecoverAt: codexQuotaProbeNextRecoverAt(rateLimit, false),
			}
		}
		if hasWindowUsage {
			return &cliproxyauth.QuotaProbeResult{Recovered: true}
		}
	}

	return &cliproxyauth.QuotaProbeResult{
		Recovered:     false,
		NextRecoverAt: nextRecoverAt,
	}
}

func codexQuotaProbeNextRecoverAt(rateLimit gjson.Result, exhaustedOnly bool) time.Time {
	nextRecoverAt := time.Time{}
	for _, path := range []string{"primary_window", "secondary_window"} {
		window := rateLimit.Get(path)
		if !window.Exists() {
			continue
		}
		if exhaustedOnly {
			usedPercent := window.Get("used_percent")
			if usedPercent.Exists() && usedPercent.Float() < 100 {
				continue
			}
		}
		if resetAt := codexQuotaWindowResetAt(window, time.Now()); !resetAt.IsZero() {
			if nextRecoverAt.IsZero() || resetAt.Before(nextRecoverAt) {
				nextRecoverAt = resetAt
			}
		}
	}
	return nextRecoverAt
}

func codexQuotaWindowResetAt(window gjson.Result, now time.Time) time.Time {
	if !window.Exists() {
		return time.Time{}
	}
	if resetAt := window.Get("reset_at").Int(); resetAt > 0 {
		resetAtTime := time.Unix(resetAt, 0)
		if resetAtTime.After(now) {
			return resetAtTime
		}
	}
	if afterSeconds := window.Get("reset_after_seconds").Int(); afterSeconds > 0 {
		return now.Add(time.Duration(afterSeconds) * time.Second)
	}
	return time.Time{}
}
