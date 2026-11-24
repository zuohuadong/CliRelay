package executor

import (
	"net/http"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// Codex rejects a turn when the selected model is momentarily overloaded. The
// rejection is model-scoped, short-lived, and arrives in two shapes:
//
//	HTTP 400/429  {"error":{"message":"Selected model is at capacity. Please try a different model."}}
//	HTTP 200 SSE  event: error / response.failed carrying the same message
//
// Treating it as a plain failure is what leaves a client with "Selected model
// is at capacity" although another attempt a second later would have been
// served. Both paths below classify it as a transient capacity rejection.
//
// Ported from CLIProxyAPI v8 (36973d4a6, 3ae9093da, 9a35fa12b).

const (
	// codexCapacityRetryDelaySeconds is how long one same-account retry waits
	// before asking again. Capacity windows are short; a longer pause only adds
	// latency to a turn that is already late.
	codexCapacityRetryDelaySeconds = 1

	// maxCodexBootstrapFrames and maxCodexBootstrapBytes bound how much of a
	// stream is held back before it is forwarded regardless of its content.
	maxCodexBootstrapFrames = 64
	maxCodexBootstrapBytes  = 256 << 10
)

// isCodexTransientCapacityError reports whether a Codex error body is a
// transient capacity rejection. An explicit error code other than the capacity
// codes keeps its existing handling, so a generic rate_limit_exceeded rejection
// is never reclassified by message text alone.
func isCodexTransientCapacityError(errorBody []byte) bool {
	if code := codexCapacityErrorCode(errorBody); code != "" &&
		!strings.EqualFold(code, "turn_rate_limit_exceeded") &&
		!strings.EqualFold(code, "model_at_capacity") &&
		!strings.EqualFold(code, "model_is_at_capacity") {
		return false
	}
	return isCodexModelCapacityMessage(errorBody) || isCodexTurnRateLimitError(errorBody)
}

// codexCapacityErrorCode returns the dedicated error code across the layouts
// the Codex upstream uses.
func codexCapacityErrorCode(errorBody []byte) string {
	for _, path := range []string{"error.code", "code", "body.error.code"} {
		if code := strings.TrimSpace(gjson.GetBytes(errorBody, path).String()); code != "" {
			return code
		}
	}
	return ""
}

// isCodexModelCapacityMessage covers the legacy "model is at capacity" wording.
func isCodexModelCapacityMessage(errorBody []byte) bool {
	if len(errorBody) == 0 {
		return false
	}
	candidates := []string{
		gjson.GetBytes(errorBody, "error.message").String(),
		gjson.GetBytes(errorBody, "message").String(),
		string(errorBody),
	}
	for _, candidate := range candidates {
		lower := strings.ToLower(strings.TrimSpace(candidate))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "model is at capacity") ||
			strings.Contains(lower, "model_at_capacity") ||
			strings.Contains(lower, "model_is_at_capacity") ||
			(strings.Contains(lower, "model") && strings.Contains(lower, "at capacity")) {
			return true
		}
	}
	return false
}

// isCodexTurnRateLimitError reports Codex's per-turn admission rejection: the
// account's quota is not exhausted, the turn simply arrived when the service was
// full. The stable signal is the dedicated code; without one, a
// rate_limit_error that says something is at capacity is accepted.
func isCodexTurnRateLimitError(errorBody []byte) bool {
	if len(errorBody) == 0 {
		return false
	}
	hasOtherCode := false
	for _, path := range []string{"error.code", "code", "body.error.code"} {
		candidate := strings.TrimSpace(gjson.GetBytes(errorBody, path).String())
		if candidate == "" {
			continue
		}
		if strings.EqualFold(candidate, "turn_rate_limit_exceeded") {
			return true
		}
		hasOtherCode = true
	}
	if hasOtherCode {
		return false
	}
	rateLimitTyped := false
	for _, path := range []string{"error.type", "type", "body.error.type"} {
		if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(errorBody, path).String()), "rate_limit_error") {
			rateLimitTyped = true
			break
		}
	}
	if !rateLimitTyped {
		return false
	}
	for _, path := range []string{"error.message", "message", "body.error.message"} {
		if strings.Contains(strings.ToLower(strings.TrimSpace(gjson.GetBytes(errorBody, path).String())), "at capacity") {
			return true
		}
	}
	return false
}

// hasCodexMeaningfulOutputDelta reports whether an event carries generated
// content. Only these events justify committing a buffered bootstrap: a
// handshake followed by a capacity rejection must stay retryable.
func hasCodexMeaningfulOutputDelta(eventData []byte) bool {
	switch gjson.GetBytes(eventData, "type").String() {
	case "response.output_text.delta", "response.refusal.delta",
		"response.reasoning_text.delta", "response.reasoning_summary_text.delta",
		"response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		return strings.TrimSpace(gjson.GetBytes(eventData, "delta").String()) != ""
	}
	return false
}

// isCodexCapacityStatusErr reports whether err is a capacity rejection the
// attempt may retry, in either of the shapes the executors produce.
func isCodexCapacityStatusErr(err error) bool {
	switch typed := err.(type) {
	case statusErr:
		return typed.code == http.StatusTooManyRequests && isCodexTransientCapacityError([]byte(typed.msg))
	case *cliproxyauth.Error:
		if typed == nil || typed.HTTPStatus != http.StatusTooManyRequests {
			return false
		}
		return isCodexTransientCapacityError([]byte(typed.Message))
	}
	return false
}
