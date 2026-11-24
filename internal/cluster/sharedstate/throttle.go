package sharedstate

import "time"

// Shared login throttle buckets hold the semantics of the node-local throttle
// in the management handlers: two sliding windows (short burst, long daily
// ceiling), a quiet period (reset-after) that clears counters and escalation,
// and blocks whose length climbs a ladder indexed by how many blocks came
// before.

// ThrottleWindow is one sliding window of a throttle bucket. A zero Limit
// disables it.
type ThrottleWindow struct {
	Limit  int
	Window time.Duration
	Slots  int
}

// ThrottleSpec is a bucket's policy.
type ThrottleSpec struct {
	Short      ThrottleWindow
	Long       ThrottleWindow
	Backoff    []time.Duration
	ResetAfter time.Duration
	// Retention is how long an idle bucket is kept; a live block always
	// extends it to the block's end.
	Retention time.Duration
}

// ThrottleState is a bucket's standing after a charge or peek.
type ThrottleState struct {
	Blocked    bool
	RetryAfter time.Duration
	Generation int
	ShortCount int
	LongCount  int
	NewlyArmed bool
}

// slotWidth is Window/Slots, or zero when the window is disabled. A width
// below one millisecond is rounded up so no caller divides by zero.
func slotWidth(w ThrottleWindow) time.Duration {
	if w.Limit <= 0 || w.Window <= 0 || w.Slots <= 0 {
		return 0
	}
	width := w.Window / time.Duration(w.Slots)
	if width < time.Millisecond {
		width = time.Millisecond
	}
	return width
}
