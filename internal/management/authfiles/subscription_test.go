package authfiles

import (
	"encoding/json"
	"testing"
	"time"
)

func TestExtractLastRefreshTimestampAcceptsLegacyKeys(t *testing.T) {
	got, ok := ExtractLastRefreshTimestamp(map[string]any{
		"lastRefreshedAt": json.Number("1772449800"),
	})
	if !ok {
		t.Fatal("expected last refresh timestamp")
	}
	want := time.Unix(1772449800, 0).UTC()
	if !got.Equal(want) {
		t.Fatalf("timestamp = %s, want %s", got, want)
	}
}

func TestParseSubscriptionTimestampValueAcceptsMilliseconds(t *testing.T) {
	want := time.Date(2026, 4, 1, 9, 30, 0, 0, time.UTC)
	got, ok := ParseSubscriptionTimestampValue(want.UnixMilli())
	if !ok {
		t.Fatal("expected subscription timestamp")
	}
	if !got.Equal(want) {
		t.Fatalf("timestamp = %s, want %s", got, want)
	}
}

func TestAddSubscriptionFieldsUsesExplicitExpiration(t *testing.T) {
	now := time.Date(2026, 4, 1, 9, 30, 0, 0, time.UTC)
	expiresAt := now.Add(90 * time.Minute)
	entry := map[string]any{}

	AddSubscriptionFields(entry, map[string]any{
		"subscriptionExpiresAt": expiresAt.Format(time.RFC3339),
	}, now)

	if got, _ := entry["subscription_expires_at"].(string); got != expiresAt.Format(time.RFC3339) {
		t.Fatalf("subscription_expires_at = %q, want %q", got, expiresAt.Format(time.RFC3339))
	}
	if got, _ := entry["subscription_remaining_minutes"].(int64); got != 90 {
		t.Fatalf("subscription_remaining_minutes = %d, want 90", got)
	}
	if expired, _ := entry["subscription_expired"].(bool); expired {
		t.Fatal("subscription_expired = true, want false")
	}
}

func TestAddSubscriptionFieldsDerivesExpirationFromStartAndPeriod(t *testing.T) {
	now := time.Date(2026, 4, 15, 9, 30, 0, 0, time.UTC)
	startedAt := time.Date(2026, 4, 1, 9, 30, 0, 0, time.UTC)
	entry := map[string]any{}

	AddSubscriptionFields(entry, map[string]any{
		"subscription_start_at": startedAt.Format(time.RFC3339),
		"subscriptionPeriod":    "annual",
	}, now)

	wantExpiresAt := startedAt.AddDate(1, 0, 0)
	if got, _ := entry["subscription_period"].(string); got != "yearly" {
		t.Fatalf("subscription_period = %q, want yearly", got)
	}
	if got, _ := entry["subscription_expires_at"].(string); got != wantExpiresAt.Format(time.RFC3339) {
		t.Fatalf("subscription_expires_at = %q, want %q", got, wantExpiresAt.Format(time.RFC3339))
	}
}

func TestClearSubscriptionMetadataRemovesStoredAndDerivedKeys(t *testing.T) {
	meta := map[string]any{
		"subscription_started_at":        "2026-04-01T09:30:00Z",
		"subscriptionStartedAt":          "2026-04-01T09:30:00Z",
		"subscription_period":            "monthly",
		"subscriptionPeriod":             "monthly",
		"subscription_expires_at":        "2026-05-01T09:30:00Z",
		"subscriptionExpiresAt":          "2026-05-01T09:30:00Z",
		"subscription_remaining_minutes": 10,
		"subscriptionRemainingDays":      1,
		"subscription_expired":           false,
	}

	ClearSubscriptionMetadata(meta)

	for key := range meta {
		t.Fatalf("unexpected remaining key %q", key)
	}
}
