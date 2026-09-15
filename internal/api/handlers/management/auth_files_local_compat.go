package management

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func addAuthFileTokenHealth(entry gin.H, provider string, metadata map[string]any, disabled bool, now time.Time) {
	if entry == nil || metadata == nil || !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return
	}
	auth := &coreauth.Auth{Metadata: metadata}
	expiresAt, ok := auth.ExpirationTime()
	if !ok {
		return
	}
	expiresAt = expiresAt.UTC()
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	secondsLeft := int64(expiresAt.Sub(now).Seconds())
	daysLeft := secondsLeft / int64(24*time.Hour/time.Second)
	if secondsLeft < 0 {
		daysLeft = -(((-secondsLeft) + int64(24*time.Hour/time.Second) - 1) / int64(24*time.Hour/time.Second))
	}
	health := "ok"
	switch {
	case disabled:
		health = "disabled"
	case !expiresAt.After(now):
		health = "expired"
	case expiresAt.Sub(now) <= 24*time.Hour:
		health = "critical"
	case expiresAt.Sub(now) <= 72*time.Hour:
		health = "warning"
	}
	entry["token_health"] = health
	entry["token_expires_at"] = expiresAt
	entry["token_expires_at_ms"] = expiresAt.UnixMilli()
	entry["token_seconds_left"] = secondsLeft
	entry["token_days_left"] = daysLeft
	if lastRefresh, okRefresh := extractLastRefreshTimestamp(metadata); okRefresh && !lastRefresh.IsZero() {
		entry["token_last_refresh"] = lastRefresh.UTC()
		entry["token_last_refresh_ms"] = lastRefresh.UTC().UnixMilli()
	}
}

func addAuthFileSubscriptionFields(entry gin.H, metadata map[string]any) {
	if entry == nil || metadata == nil {
		return
	}
	if startedAt := metadataString(metadata, "subscription_started_at", "subscriptionStartedAt", "subscription_start_at", "subscriptionStartAt"); startedAt != "" {
		entry["subscription_started_at"] = startedAt
	}
	if period := metadataString(metadata, "subscription_period", "subscriptionPeriod"); period != "" {
		entry["subscription_period"] = period
	}
	if expiresAt := metadataString(metadata, "subscription_expires_at", "subscriptionExpiresAt"); expiresAt != "" {
		entry["subscription_expires_at"] = expiresAt
	}
	if startedAtMs, ok := metadataPositiveInt64(metadata, "subscription_started_at_ms", "subscriptionStartedAtMs"); ok {
		entry["subscription_started_at_ms"] = startedAtMs
	}
	if expiresAtMs, ok := metadataPositiveInt64(metadata, "subscription_expires_at_ms", "subscriptionExpiresAtMs"); ok {
		entry["subscription_expires_at_ms"] = expiresAtMs
	}
}

func metadataPositiveInt64(metadata map[string]any, keys ...string) (int64, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	for _, key := range keys {
		switch value := metadata[key].(type) {
		case float64:
			if value > 0 && value == float64(int64(value)) {
				return int64(value), true
			}
		case int64:
			if value > 0 {
				return value, true
			}
		case int:
			if value > 0 {
				return int64(value), true
			}
		case json.Number:
			parsed, err := value.Int64()
			if err == nil && parsed > 0 {
				return parsed, true
			}
		case string:
			parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err == nil && parsed > 0 {
				return parsed, true
			}
		}
	}
	return 0, false
}

func parseAuthFileTimestamp(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		if typed.IsZero() {
			return time.Time{}, false
		}
		return typed.UTC(), true
	case float64:
		return unixLikeTimestamp(typed)
	case int64:
		return unixLikeTimestamp(float64(typed))
	case int:
		return unixLikeTimestamp(float64(typed))
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return time.Time{}, false
		}
		return unixLikeTimestamp(parsed)
	default:
		return parseLastRefreshValue(value)
	}
}

func unixLikeTimestamp(value float64) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	if value >= 1e12 {
		return time.UnixMilli(int64(value)).UTC(), true
	}
	return time.Unix(int64(value), 0).UTC(), true
}

func extractCodexIDTokenClaimsFromMetadata(provider string, metadata map[string]any) gin.H {
	if metadata == nil || !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return nil
	}
	idTokenRaw, ok := metadata["id_token"].(string)
	if !ok {
		return nil
	}
	idToken := strings.TrimSpace(idTokenRaw)
	if idToken == "" {
		return nil
	}
	claims, err := codex.ParseJWTToken(idToken)
	if err != nil || claims == nil {
		return nil
	}

	result := gin.H{}
	if v := strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID); v != "" {
		result["chatgpt_account_id"] = v
	}
	if v := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); v != "" {
		result["plan_type"] = v
	}
	if v := claims.CodexAuthInfo.ChatgptSubscriptionActiveStart; v != nil {
		if ts, ok := parseAuthFileTimestamp(v); ok {
			result["chatgpt_subscription_active_start"] = ts
		} else {
			result["chatgpt_subscription_active_start"] = v
		}
	}
	if v := claims.CodexAuthInfo.ChatgptSubscriptionActiveUntil; v != nil {
		if ts, ok := parseAuthFileTimestamp(v); ok {
			result["chatgpt_subscription_active_until"] = ts
		} else {
			result["chatgpt_subscription_active_until"] = v
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func authCodexFastModeValue(auth *coreauth.Auth) (bool, bool) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false, false
	}
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["codex_fast_mode"]); raw != "" {
			if parsed, errParse := strconv.ParseBool(raw); errParse == nil {
				return parsed, true
			}
		}
	}
	if auth.Metadata == nil {
		return false, true
	}
	raw, ok := auth.Metadata["codex_fast_mode"]
	if !ok || raw == nil {
		return false, true
	}
	if parsed, ok := authFileBoolValue(raw); ok {
		return parsed, true
	}
	return false, true
}

func backfillCodexAccountIDInData(data []byte) ([]byte, bool) {
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil || metadata == nil {
		return data, false
	}
	provider, _ := metadata["type"].(string)
	if provider == "" {
		provider, _ = metadata["provider"].(string)
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return data, false
	}
	if existing, _ := metadata["account_id"].(string); strings.TrimSpace(existing) != "" {
		return data, false
	}
	if codex.AccountIDFromMetadata(metadata) == "" {
		return data, false
	}
	backfilled, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return data, false
	}
	return backfilled, true
}

func (h *Handler) bindImportedCodexAuthToEgress(ctx context.Context, egressID, authFileName string) error {
	egressID = strings.TrimSpace(egressID)
	if egressID == "" {
		return nil
	}
	service := h.egress()
	if service == nil {
		return fmt.Errorf("%w: egress network is unavailable", egress.ErrEgressRequired)
	}
	auth := h.findAuthForDelete(authFileName)
	if auth == nil {
		return fmt.Errorf("auth %q not found after import", authFileName)
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	accountID := codex.AccountIDFromMetadata(auth.Metadata)
	if accountID == "" {
		return fmt.Errorf("codex auth %q has no account_id; refresh or re-login before binding", authFileName)
	}
	identity, err := egress.StableIdentity(accountID)
	if err != nil {
		return fmt.Errorf("derive codex egress identity: %w", err)
	}
	readiness, err := service.EndpointReadiness(ctx, egressID)
	if err != nil {
		return fmt.Errorf("egress endpoint %s: %w", egressID, err)
	}
	if !readiness.RuntimeReady {
		return fmt.Errorf("egress endpoint %s is not runtime ready: %s", egressID, strings.Join(readiness.Reasons, ","))
	}
	if err := service.PutBinding(ctx, egress.Binding{Identity: identity, EndpointID: egressID, AuthFileID: auth.ID}); err != nil {
		return fmt.Errorf("bind codex egress endpoint: %w", err)
	}
	return nil
}
