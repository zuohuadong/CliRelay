package sharedstate

import "time"

// Shared auto-ban counters. A source failing authentication on several nodes is
// counted once, and exactly one node acts when it crosses the threshold: a
// plain shared counter is not enough, because two nodes can both see the count
// cross and each would write the deny rule, send the alert and bump the
// escalation counter.

// AutoBanSpec is the part of the auto-ban policy the counter needs.
type AutoBanSpec struct {
	Window    time.Duration
	Slots     int
	Threshold int
	// ClaimTTL bounds how long one node may take to act on a crossing.
	ClaimTTL time.Duration
	// Retention keeps an idle source's ban history for escalation.
	Retention time.Duration
}

// AutoBanCharge is the result of charging one failure.
type AutoBanCharge struct {
	Failures int
	Bans     int
	// AlreadyBanned is true while a ban or another node's claim is in force;
	// the failure was not counted.
	AlreadyBanned bool
	// Claimed is true when this failure crossed the threshold and this node
	// must now act on it.
	Claimed bool
}
