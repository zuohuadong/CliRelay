package auth

import (
	"net/http"
	"testing"
)

func TestShouldStopMixedProviderFallback_EmptyStreamRetryable(t *testing.T) {
	emptyStreamErr := &Error{
		Code:       "empty_stream",
		Message:    "upstream stream closed before first payload",
		Retryable:  true,
		HTTPStatus: 0,
	}
	// empty_stream is retryable, so fallback should NOT stop
	if shouldStopMixedProviderFallback("astron-code", "gpt-5.3-codex", emptyStreamErr) {
		t.Error("expected fallback to continue for retryable empty_stream error, but it stopped")
	}
}

func TestShouldStopMixedProviderFallback_500Stops(t *testing.T) {
	err500 := &Error{
		Code:       "upstream_error",
		Message:    "internal server error",
		Retryable:  false,
		HTTPStatus: http.StatusInternalServerError,
	}
	// non-retryable 500 should stop fallback
	if !shouldStopMixedProviderFallback("astron-code", "gpt-5.3-codex", err500) {
		t.Error("expected fallback to stop for non-retryable 500 error, but it continued")
	}
}

func TestShouldStopMixedProviderFallback_NonAstronProvider(t *testing.T) {
	err := &Error{
		Code:      "empty_stream",
		Message:   "upstream stream closed before first payload",
		Retryable: true,
	}
	if shouldStopMixedProviderFallback("bigmodel-coding", "gpt-5.3-codex", err) {
		t.Error("expected fallback to continue for non-astron-code provider, but it stopped")
	}
}

func TestShouldStopMixedProviderFallback_NonCodexModel(t *testing.T) {
	err := &Error{
		Code:      "empty_stream",
		Message:   "upstream stream closed before first payload",
		Retryable: true,
	}
	if shouldStopMixedProviderFallback("astron-code", "gpt-4o", err) {
		t.Error("expected fallback to continue for non-codex model, but it stopped")
	}
}

func TestShouldStopMixedProviderFallback_Retryable502(t *testing.T) {
	err := &Error{
		Code:       "bad_gateway",
		Message:    "bad gateway",
		Retryable:  true,
		HTTPStatus: http.StatusBadGateway,
	}
	// Retryable 502 should continue fallback
	if shouldStopMixedProviderFallback("astron-code", "gpt-5.3-codex", err) {
		t.Error("expected fallback to continue for retryable 502 error, but it stopped")
	}
}

func TestShouldStopMixedProviderFallback_NonRetryable502(t *testing.T) {
	err := &Error{
		Code:       "bad_gateway",
		Message:    "bad gateway",
		Retryable:  false,
		HTTPStatus: http.StatusBadGateway,
	}
	// Non-retryable 502 should stop fallback
	if !shouldStopMixedProviderFallback("astron-code", "gpt-5.3-codex", err) {
		t.Error("expected fallback to stop for non-retryable 502 error, but it continued")
	}
}
