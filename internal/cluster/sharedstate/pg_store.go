package sharedstate

import (
	"context"
	"encoding/json"
	"time"

	pgredis "github.com/zuohuadong/pgredis-go"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster/sharedredis"
)

// PGStore keeps the shared cluster state that would otherwise need Redis in the
// cluster database, through github.com/zuohuadong/pgredis-go.
//
// It covers only the features whose semantics are a single PostgreSQL
// statement or a bounded composition of them. Account concurrency slots are
// counted by the caller's own pool (see clusterruntime); this store adds
// synthetic identifier minting. Rate limits, login throttles, auto-bans and
// session affinity still need the Redis-backed Store: their windows are Lua
// scripts with several dependent steps, and a deployment without Redis keeps
// the documented per-node behaviour for them.
type PGStore struct {
	store *pgredis.Store
}

// NewPGStore wraps a pgredis store. A nil store makes every method report
// unavailability, matching the Redis-backed Store's contract.
func NewPGStore(store *pgredis.Store) *PGStore {
	return &PGStore{store: store}
}

// Available reports whether the shared state can be used right now.
func (s *PGStore) Available() bool { return s != nil && s.store != nil }

// MintID returns the cluster-wide identifier for (namespace, key), storing
// candidate when there is none.
//
// The candidate is what the caller already generates; this only decides which
// candidate wins, so the identifier keeps the shape the client fingerprint
// expects. sliding extends an existing identifier's TTL on read, for caches
// whose entries live as long as they keep being used.
func (s *PGStore) MintID(ctx context.Context, namespace, key, candidate string, ttl time.Duration, sliding bool) (MintedID, error) {
	if !s.Available() {
		return MintedID{}, sharedredis.ErrUnavailable
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	value, minted, remaining, err := s.store.GetOrSet(ctx, s.mintKey(namespace, key), candidate, pgredis.TTL(ttl), sliding)
	if err != nil {
		return MintedID{}, err
	}
	var id string
	if err := json.Unmarshal(value, &id); err != nil {
		return MintedID{}, err
	}
	if id == "" {
		return MintedID{}, sharedredis.ErrUnavailable
	}
	if remaining <= 0 || remaining > ttl {
		// A value without expiry (written by something else) and a TTL that
		// somehow exceeds what we set both clamp to ttl, as the Redis path does.
		remaining = ttl
	}
	return MintedID{ID: id, TTL: remaining, Minted: minted}, nil
}

// mintKey mirrors the Redis key layout so an operator reading both stores sees
// the same names, without the Redis-only prefix.
func (s *PGStore) mintKey(namespace, key string) string {
	return "sid:" + namespace + ":" + sharedredis.HashPart(namespace, key)
}
