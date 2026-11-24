package sharedstate

// Per-subject request and token windows are shared by every node, so each node
// does not admit the full limit: N nodes would otherwise allow N times it.

// Admission is the cluster-wide view of one admitted request.
type Admission struct {
	// RPM and TPM are the subject's trailing-minute request and token counts
	// across the cluster, including this request.
	RPM float64
	TPM float64
	// SlotTaken reports whether a concurrency slot is now held for the
	// request. Held is the number of slots in use, including this one when
	// taken.
	SlotTaken bool
	Held      int
	// Release gives the slot back. It is safe to call more than once and is a
	// no-op when no slot was taken.
	Release func()
}

// Rate is one subject's cluster-wide trailing-minute usage.
type Rate struct {
	RPM float64
	TPM float64
}
