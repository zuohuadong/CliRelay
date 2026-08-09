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
