package usage

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestQueryDashboardTrendsReturnsFixedDailyBuckets(t *testing.T) {
	initTestUsageDB(t, config.RequestLogStorageConfig{StoreContent: false})

	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1)
	InsertLog("", "", "gpt-test", "codex", "codex", "auth-1", false, yesterday, 120, 20, TokenStats{
		InputTokens:  10,
		OutputTokens: 20,
		TotalTokens:  30,
	}, "", "")
	InsertLog("", "", "gpt-test", "codex", "codex", "auth-1", true, now, 180, 30, TokenStats{
		InputTokens:  40,
		OutputTokens: 50,
		TotalTokens:  90,
	}, "", "")

	trends, err := QueryDashboardTrends(7)
	if err != nil {
		t.Fatalf("QueryDashboardTrends() error = %v", err)
	}

	if len(trends.RequestVolume) != 7 {
		t.Fatalf("request_volume buckets = %d, want 7", len(trends.RequestVolume))
	}
	if len(trends.SuccessRate) != 7 {
		t.Fatalf("success_rate buckets = %d, want 7", len(trends.SuccessRate))
	}
	if len(trends.TotalTokens) != 7 {
		t.Fatalf("total_tokens buckets = %d, want 7", len(trends.TotalTokens))
	}
	if len(trends.FailedRequests) != 7 {
		t.Fatalf("failed_requests buckets = %d, want 7", len(trends.FailedRequests))
	}
	if len(trends.ThroughputSeries) != 7 {
		t.Fatalf("throughput_series buckets = %d, want 7", len(trends.ThroughputSeries))
	}

	todayLabel := localDayKeyAt(now)
	yesterdayLabel := localDayKeyAt(yesterday)
	todayRequests := findTrendValue(t, trends.RequestVolume, todayLabel)
	yesterdayRequests := findTrendValue(t, trends.RequestVolume, yesterdayLabel)
	todayFailed := findTrendValue(t, trends.FailedRequests, todayLabel)
	todaySuccessRate := findTrendValue(t, trends.SuccessRate, todayLabel)
	todayTokens := findTrendValue(t, trends.TotalTokens, todayLabel)

	if todayRequests != 1 {
		t.Fatalf("today requests = %.0f, want 1", todayRequests)
	}
	if yesterdayRequests != 1 {
		t.Fatalf("yesterday requests = %.0f, want 1", yesterdayRequests)
	}
	if todayFailed != 1 {
		t.Fatalf("today failed = %.0f, want 1", todayFailed)
	}
	if todaySuccessRate != 0 {
		t.Fatalf("today success rate = %.2f, want 0", todaySuccessRate)
	}
	if todayTokens != 90 {
		t.Fatalf("today tokens = %.0f, want 90", todayTokens)
	}
}

func findTrendValue(t *testing.T, points []DashboardTrendPoint, label string) float64 {
	t.Helper()
	for _, point := range points {
		if point.Label == label {
			return point.Value
		}
	}
	t.Fatalf("missing trend point with label %q", label)
	return 0
}
