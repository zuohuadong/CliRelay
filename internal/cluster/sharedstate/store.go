// Package sharedstate keeps the counters, leases and bindings that must be
// global across the nodes of a cluster in PostgreSQL. Callers choose how to
// fall back when the shared database is unavailable.
package sharedstate

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

// Default lease timing. A lease is renewed every LeaseTTL/3 while the request
// holding it runs, so a crashed node leaks each of its slots for at most one
// lease TTL.
const (
	DefaultLeaseTTL = 90 * time.Second
)

// Options tunes a Store. Zero values take the defaults.
type Options struct {
	// NodeID prefixes lease members, so a stuck slot can be traced to a node.
	NodeID string
	// LeaseTTL is how long a concurrency slot survives without renewal.
	LeaseTTL time.Duration
	// FlushInterval is how often asynchronously counted requests and tokens
	// are written. Defaults to one second.
	FlushInterval time.Duration
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Store is the process handle on shared cluster state. A nil *Store, or one
// whose database is nil, is permanently unavailable.
type Store struct {
	db     *sql.DB
	nodeID string
	now    func() time.Time

	leaseTTL time.Duration
	keeper   *leaseKeeper
	counts   *countBatcher
	slots    *accountSlotPool

	closeOnce   sync.Once
	stopCleanup context.CancelFunc
	cleanupDone chan struct{}
}

// New builds a store on the migrated runtime database without owning the pool.
func New(db *sql.DB, opts Options) *Store {
	if opts.LeaseTTL <= 0 {
		opts.LeaseTTL = DefaultLeaseTTL
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NodeID == "" {
		opts.NodeID = "local"
	}
	s := &Store{
		db:       db,
		nodeID:   opts.NodeID,
		now:      opts.Now,
		leaseTTL: opts.LeaseTTL,
	}
	s.keeper = newLeaseKeeper(s)
	s.counts = newCountBatcher(s, opts.FlushInterval)
	s.slots = newAccountSlotPool(s)
	if db != nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.stopCleanup, s.cleanupDone = cancel, make(chan struct{})
		go s.cleanupLoop(ctx)
	}
	return s
}

// Available reports whether shared state is configured. Operations report
// connectivity errors independently.
func (s *Store) Available() bool {
	return s != nil && s.db != nil
}

func (s *Store) localTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Flush writes asynchronously counted requests and tokens now.
func (s *Store) Flush() {
	if s == nil {
		return
	}
	s.counts.flush()
}

// Close flushes pending counters, stops renewing leases and gives back the
// ones still held, so a node that shuts down cleanly frees its slots at once
// instead of after a lease TTL.
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.stopCleanup != nil {
			s.stopCleanup()
			<-s.cleanupDone
		}
		s.counts.close()
		s.keeper.close()
	})
}
