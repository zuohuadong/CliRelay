package sharedstate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/testutil/postgrestest"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	// Mid-minute, so tests that do not care about the minute boundary are not
	// affected by it.
	return &fakeClock{now: time.Date(2026, 9, 24, 12, 0, 30, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

type testEnv struct {
	db    *sql.DB
	clock *fakeClock
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, _ := postgrestest.RuntimeState(t)
	return &testEnv{db: db, clock: newFakeClock()}
}

// node builds one node's store on the shared database.
func (e *testEnv) node(t *testing.T, nodeID string) *Store {
	t.Helper()
	store := New(e.db, Options{NodeID: nodeID, Now: e.clock.Now, FlushInterval: time.Hour})
	t.Cleanup(store.Close)
	return store
}

func (e *testEnv) assertExpiry(t *testing.T, table string, want int, horizon time.Duration) {
	t.Helper()
	var count, valid int
	now := e.clock.Now()
	err := e.db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE expires_at > $1 AND expires_at <= $2)
		FROM `+table, now, now.Add(horizon)).Scan(&count, &valid)
	if err != nil || count != want || valid != want {
		t.Fatalf("%s: count=%d with valid expiry=%d, want %d, err=%v", table, count, valid, want, err)
	}
}

// unique returns a per-test identifier.
func unique(t *testing.T, label string) string {
	var buf [6]byte
	_, _ = rand.Read(buf[:])
	return t.Name() + "/" + label + "/" + hex.EncodeToString(buf[:])
}

var bg = context.Background()
