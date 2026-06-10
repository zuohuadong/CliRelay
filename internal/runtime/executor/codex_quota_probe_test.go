package executor

import (
	"strconv"
	"testing"
	"time"
)

func TestParseCodexQuotaProbeLimitReached(t *testing.T) {
	body := []byte(`{"rate_limit":{"limit_reached":true,"primary_window":{"used_percent":100,"reset_after_seconds":120}}}`)

	result := parseCodexQuotaProbe(body)
	if result == nil {
		t.Fatal("expected quota probe result")
	}
	if result.Recovered {
		t.Fatal("limit_reached=true must not be treated as recovered")
	}
	if result.NextRecoverAt.IsZero() {
		t.Fatal("expected next recovery timestamp from reset_after_seconds")
	}
}

func TestParseCodexQuotaProbeAllowed(t *testing.T) {
	body := []byte(`{"rate_limit":{"allowed":true,"primary_window":{"used_percent":10}}}`)

	result := parseCodexQuotaProbe(body)
	if result == nil {
		t.Fatal("expected quota probe result")
	}
	if !result.Recovered {
		t.Fatal("allowed=true should be treated as recovered")
	}
}

func TestParseCodexQuotaProbeExhaustedWindow(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).Unix()
	body := []byte(`{"rate_limit":{"allowed":true,"primary_window":{"used_percent":100,"reset_at":` + strconv.FormatInt(resetAt, 10) + `}}}`)

	result := parseCodexQuotaProbe(body)
	if result == nil {
		t.Fatal("expected quota probe result")
	}
	if result.Recovered {
		t.Fatal("exhausted window should not be treated as recovered")
	}
	if result.NextRecoverAt.IsZero() {
		t.Fatal("expected next recovery timestamp")
	}
}
