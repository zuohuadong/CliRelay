package usage

import (
	"testing"
	"time"
)

func TestCurrentMonthlyBillingCycleUsesCalendarMonthWithoutAnchor(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 30, 0, 0, time.UTC)
	start, end := CurrentMonthlyBillingCycle("", now)
	if want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC); !start.Equal(want) {
		t.Fatalf("start = %s, want %s", start, want)
	}
	if want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("end = %s, want %s", end, want)
	}
}

func TestCurrentMonthlyBillingCycleClampsMonthEndAnchor(t *testing.T) {
	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	start, end := CurrentMonthlyBillingCycle("2026-01-31T08:30:00Z", now)
	if want := time.Date(2026, 1, 31, 8, 30, 0, 0, time.UTC); !start.Equal(want) {
		t.Fatalf("start = %s, want %s", start, want)
	}
	if want := time.Date(2026, 2, 28, 8, 30, 0, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("end = %s, want %s", end, want)
	}
}

func TestCurrentMonthlyBillingCycleHandlesCrossYearAnchor(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	start, end := CurrentMonthlyBillingCycle("2025-12-31T23:00:00Z", now)
	if want := time.Date(2025, 12, 31, 23, 0, 0, 0, time.UTC); !start.Equal(want) {
		t.Fatalf("start = %s, want %s", start, want)
	}
	if want := time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("end = %s, want %s", end, want)
	}
}
