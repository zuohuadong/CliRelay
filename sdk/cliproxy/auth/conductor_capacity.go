package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	defaultCapacitySameAccountRetries = 3
	capacitySameAccountRetryBaseDelay = 500 * time.Millisecond
	capacitySameAccountRetryMaxDelay  = 8 * time.Second
)

func isUpstreamCapacityOverloadError(err error) bool {
	if err == nil {
		return false
	}

	type errorCodeProvider interface {
		ErrorCode() string
	}
	var errorCode string
	var provider errorCodeProvider
	if errors.As(err, &provider) && provider != nil {
		errorCode = strings.ToLower(strings.TrimSpace(provider.ErrorCode()))
	}

	lower := strings.ToLower(err.Error())
	if errorCode == "server_is_overloaded" || errorCode == "slow_down" {
		return true
	}
	for _, marker := range [...]string{
		"server_is_overloaded",
		"selected model is at capacity",
		"model is at capacity. please try a different model",
		"our servers are currently overloaded",
		"slow_down",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	status := statusCodeFromError(err)
	if status != http.StatusServiceUnavailable && status != http.StatusTooManyRequests {
		return false
	}
	return strings.Contains(lower, "service_unavailable_error") ||
		strings.Contains(lower, "capacity") ||
		strings.Contains(lower, "overload")
}

func capacitySameAccountRetryDelay(err error, attempt int) time.Duration {
	if retryAfter := retryAfterFromError(err); retryAfter != nil && *retryAfter >= 0 && *retryAfter <= capacitySameAccountRetryMaxDelay {
		return *retryAfter
	}
	if attempt < 0 {
		attempt = 0
	}
	delay := capacitySameAccountRetryBaseDelay
	for i := 0; i < attempt && delay < capacitySameAccountRetryMaxDelay; i++ {
		delay *= 2
		if delay > capacitySameAccountRetryMaxDelay {
			delay = capacitySameAccountRetryMaxDelay
		}
	}
	return delay
}

func waitForCapacitySameAccountRetry(ctx context.Context, err error, attempt int) error {
	delay := capacitySameAccountRetryDelay(err, attempt)
	if delay <= 0 {
		if ctx != nil {
			return ctx.Err()
		}
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	if ctx == nil {
		<-timer.C
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *Manager) effectiveCapacitySameAccountRetries(auth *Auth) int {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.AuthKind() != AuthKindOAuth {
		return 0
	}
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil {
		return 0
	}
	retries := cfg.CapacitySameAccountRetries
	if cfg.Codex.CapacitySameAccountRetries != nil {
		retries = cfg.Codex.CapacitySameAccountRetries
	}
	if retries != nil {
		if *retries <= 0 {
			return 0
		}
		return *retries
	}
	if cfg.Codex.StreamBootstrapBuffering {
		return defaultCapacitySameAccountRetries
	}
	return 0
}
