package auth

import (
	"testing"
)

// Google CloudCode returns "Resource has been exhausted" for ordinary
// per-minute throttling. Reading that as an exhausted quota suspended models on
// accounts that still had their full allowance, producing a suspend -> resume ->
// suspend loop within seconds.
func TestTransientRateLimitIsNotQuotaExhaustion(t *testing.T) {
	throttles := []string{
		"Resource has been exhausted (e.g. check quota).",
		`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`,
		"rate limit exceeded",
		"Too Many Requests",
	}
	for _, message := range throttles {
		if !isTransientRateLimitError(&Error{Message: message}) {
			t.Fatalf("%q should be treated as throttling, not exhaustion", message)
		}
	}
}

// An explicitly named quota keeps the old behaviour: it really is exhausted and
// the model should be parked until the window resets.
func TestNamedQuotaExhaustionStillCountsAsExhaustion(t *testing.T) {
	exhausted := []string{
		`{"error":{"details":[{"reason":"QUOTA_EXHAUSTED"}]}}`,
		`{"error":{"details":[{"metadata":{"quotaResetDelay":"2h1m1s"}}]}}`,
		"Quota limit 'Tokens per minute' exceeded",
		"daily quota reached",
		"usage balance exhausted",
	}
	for _, message := range exhausted {
		if isTransientRateLimitError(&Error{Message: message}) {
			t.Fatalf("%q names a quota and must stay an exhaustion", message)
		}
	}
}

// The real-world message that caused the loop: it contains the word "quota"
// only inside the generic hint, which must not tip the classification.
func TestGenericExhaustedHintMentioningQuotaIsStillThrottling(t *testing.T) {
	err := &Error{Message: `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`}
	if !isTransientRateLimitError(err) {
		t.Fatal("the parenthetical 'check quota' hint must not make this an exhaustion")
	}
}

// Nothing recognisable keeps the previous behaviour, so this change can only
// narrow which failures suspend a model.
func TestUnrecognisedMessageFallsBackToExhaustion(t *testing.T) {
	for _, message := range []string{"", "upstream exploded"} {
		if isTransientRateLimitError(&Error{Message: message}) {
			t.Fatalf("%q must not be classified as throttling", message)
		}
	}
	if isTransientRateLimitError(nil) {
		t.Fatal("nil error must not be classified as throttling")
	}
}
