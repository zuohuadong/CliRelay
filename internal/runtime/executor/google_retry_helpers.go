package executor

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/tidwall/gjson"
)

// parseRetryDelay extracts RetryInfo-style backoff durations from Google API error bodies.
func parseRetryDelay(errorBody []byte) (*time.Duration, error) {
	details := gjson.GetBytes(errorBody, "error.details")
	if details.Exists() && details.IsArray() {
		for _, detail := range details.Array() {
			typeVal := detail.Get("@type").String()
			if typeVal == "type.googleapis.com/google.rpc.RetryInfo" {
				retryDelay := detail.Get("retryDelay").String()
				if retryDelay != "" {
					duration, err := time.ParseDuration(retryDelay)
					if err != nil {
						return nil, fmt.Errorf("failed to parse duration")
					}
					return &duration, nil
				}
			}
		}

		for _, detail := range details.Array() {
			typeVal := detail.Get("@type").String()
			if typeVal == "type.googleapis.com/google.rpc.ErrorInfo" {
				quotaResetDelay := detail.Get("metadata.quotaResetDelay").String()
				if quotaResetDelay != "" {
					duration, err := time.ParseDuration(quotaResetDelay)
					if err == nil {
						return &duration, nil
					}
				}
			}
		}
	}

	message := gjson.GetBytes(errorBody, "error.message").String()
	if message != "" {
		re := regexp.MustCompile(`after\s+(\d+)s\.?`)
		if matches := re.FindStringSubmatch(message); len(matches) > 1 {
			seconds, err := strconv.Atoi(matches[1])
			if err == nil {
				duration := time.Duration(seconds) * time.Second
				return &duration, nil
			}
		}
	}

	return nil, fmt.Errorf("no RetryInfo found")
}

// parseRetryDelayWithHeaders reads the wait a Google-family upstream asked for,
// preferring the structured hint in the body and falling back to the HTTP
// Retry-After header. Capacity answers (503) ship the hint the same way a
// throttled 429 does, and a hint in the header used to be ignored entirely.
func parseRetryDelayWithHeaders(headers http.Header, errorBody []byte, now time.Time) (*time.Duration, error) {
	if wait, err := parseRetryDelay(errorBody); err == nil && wait != nil {
		return wait, nil
	}
	if wait := parseHTTPRetryAfterHeader(headers, now); wait != nil {
		return wait, nil
	}
	return nil, nil
}
