package sharedstate

import "time"

// Synthetic identifiers (upstream session ids, prompt cache keys, cloaked user
// ids) are random values minted once per conversation and then reused. In a
// cluster the value has to be minted once for the whole cluster, or two nodes
// serving one conversation send the upstream two different sessions and each
// turn misses the other's prompt cache.

// MintedID is the cluster's identifier for one key.
type MintedID struct {
	ID string
	// TTL is how long the identifier remains valid. Local caches must not keep
	// it longer, or they would outlive the shared value and diverge from the
	// node that mints its successor.
	TTL time.Duration
	// Minted reports whether the caller's candidate won.
	Minted bool
}
