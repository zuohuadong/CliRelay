package executor

import (
	"net/http"
	"testing"
	"time"
)

// A capacity refusal states its wait the same way a throttled one does, and
// previously reached the scheduler with no number at all.
func TestAntigravityStatusErrReadsCapacityRetryHints(t *testing.T) {
	rateLimitBody := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`)
	capacityBody := []byte(`{"error":{"code":503,"status":"UNAVAILABLE","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"17s"}]}}`)

	cases := []struct {
		name    string
		status  int
		headers http.Header
		body    []byte
		want    time.Duration
		wantNil bool
	}{
		{name: "429 honours Retry-After", status: http.StatusTooManyRequests, headers: http.Header{"Retry-After": []string{"45"}}, body: rateLimitBody, want: 45 * time.Second},
		{name: "503 honours Retry-After", status: http.StatusServiceUnavailable, headers: http.Header{"Retry-After": []string{"45"}}, body: rateLimitBody, want: 45 * time.Second},
		{name: "503 honours the body's RetryInfo", status: http.StatusServiceUnavailable, body: capacityBody, want: 17 * time.Second},
		{name: "body hint outranks the header", status: http.StatusServiceUnavailable, headers: http.Header{"Retry-After": []string{"90"}}, body: capacityBody, want: 17 * time.Second},
		{name: "429 with no hint stays nil", status: http.StatusTooManyRequests, body: rateLimitBody, wantNil: true},
		{name: "500 ignores hints", status: http.StatusInternalServerError, headers: http.Header{"Retry-After": []string{"45"}}, body: rateLimitBody, wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := antigravityStatusErr(tc.status, tc.headers, tc.body)
			if err.code != tc.status {
				t.Fatalf("status = %d, want %d", err.code, tc.status)
			}
			if tc.wantNil {
				if err.retryAfter != nil {
					t.Fatalf("retryAfter = %v, want nil", *err.retryAfter)
				}
				return
			}
			if err.retryAfter == nil || *err.retryAfter != tc.want {
				t.Fatalf("retryAfter = %v, want %v", err.retryAfter, tc.want)
			}
		})
	}
}

func TestParseRetryDelayWithHeadersFallsBackToTheHeader(t *testing.T) {
	now := time.Now()
	header := http.Header{"Retry-After": []string{"30"}}

	wait, err := parseRetryDelayWithHeaders(header, []byte(`{"error":{"code":429}}`), now)
	if err != nil || wait == nil || *wait != 30*time.Second {
		t.Fatalf("header fallback = %v, %v; want 30s", wait, err)
	}

	body := []byte(`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"12s"}]}}`)
	wait, err = parseRetryDelayWithHeaders(header, body, now)
	if err != nil || wait == nil || *wait != 12*time.Second {
		t.Fatalf("body precedence = %v, %v; want 12s", wait, err)
	}

	wait, err = parseRetryDelayWithHeaders(nil, []byte(`{}`), now)
	if err != nil || wait != nil {
		t.Fatalf("no hint = %v, %v; want nil", wait, err)
	}
}
