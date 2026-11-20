package sharedstate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	pgredis "github.com/zuohuadong/pgredis-go"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster/sharedredis"
)

// PGStore keeps the shared cluster state that would otherwise need Redis in the
// cluster database, through github.com/zuohuadong/pgredis-go.
//
// It covers the features whose semantics are a single PostgreSQL statement or a
// bounded composition of them: synthetic identifier minting, which is
// set-if-absent with a readback, and session affinity, which is a
// compare-and-swap repeated until it lands. Account concurrency slots are
// counted by the caller's own pool (see clusterruntime). Login throttles,
// auto-bans and quota windows still need the Redis-backed Store: they combine
// several dependent windows in one Lua decision, and a deployment without Redis
// keeps the documented per-node behaviour for them.
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

// affinityRetries bounds the compare-and-swap loop. A session key is served by
// one request at a time in practice, so the loop converges immediately; the
// bound only stops a pathological client from spinning.
const affinityRetries = 16

// affinityState mirrors the Redis hash fields, so both stores hold the same
// payload for the same binding.
type affinityState struct {
	Account string `json:"a"`
	Served  int    `json:"n"`
}

// affinityKey mirrors the Redis key layout so an operator reading both stores
// sees the same names, without the Redis-only prefix.
func pgAffinityKey(sessionKey string) string {
	return "sticky:" + sharedredis.HashPart(sessionKey)
}

// AffinityLookup returns the binding for sessionKey, counting this request and
// extending its TTL.
func (s *PGStore) AffinityLookup(ctx context.Context, sessionKey string, ttl time.Duration) (Binding, error) {
	if !s.Available() {
		return Binding{}, sharedredis.ErrUnavailable
	}
	return s.affinityServe(ctx, pgAffinityKey(sessionKey), ttl)
}

// AffinityBind binds sessionKey to account unless a binding exists, in which
// case the existing one is returned and counts this request.
func (s *PGStore) AffinityBind(ctx context.Context, sessionKey, account string, ttl time.Duration) (Binding, error) {
	if !s.Available() {
		return Binding{}, sharedredis.ErrUnavailable
	}
	key := pgAffinityKey(sessionKey)
	// The binding is written with the count this request makes, so the first
	// request through reports Served=0 like the Redis path does.
	created, err := s.store.Put(ctx, key, affinityState{Account: account, Served: 1}, pgredis.KVPutOptions{
		TTL: pgredis.TTL(ttl),
		NX:  true,
	})
	if err != nil {
		return Binding{}, err
	}
	if created {
		return Binding{Account: account, Served: 0, Found: true, Created: true}, nil
	}
	return s.affinityServe(ctx, key, ttl)
}

// AffinityRelease deletes the binding for sessionKey if it still names account.
func (s *PGStore) AffinityRelease(ctx context.Context, sessionKey, account string) error {
	if !s.Available() {
		return sharedredis.ErrUnavailable
	}
	key := pgAffinityKey(sessionKey)
	var state affinityState
	found, err := s.store.Get(ctx, key, &state)
	if err != nil || !found {
		return err
	}
	if state.Account != account {
		return nil
	}
	// Release must not drop a binding another node created after this read;
	// the compare-and-delete fails in that case, which leaves it intact.
	_, err = s.store.DeleteIfEqual(ctx, key, state)
	return err
}

// affinityServe counts one request on an existing binding and refreshes the
// TTL. The increment is a compare-and-swap retry: a concurrent request can win
// the swap, but the loser re-reads and retries, so no request goes uncounted.
func (s *PGStore) affinityServe(ctx context.Context, key string, ttl time.Duration) (Binding, error) {
	for attempt := 0; attempt < affinityRetries; attempt++ {
		var state affinityState
		found, err := s.store.Get(ctx, key, &state)
		if err != nil {
			return Binding{}, err
		}
		if !found {
			return Binding{}, nil
		}
		swapped, err := s.store.CompareAndSwapEqual(ctx, key, state,
			affinityState{Account: state.Account, Served: state.Served + 1},
			pgredis.TTL(ttl))
		if err != nil {
			return Binding{}, err
		}
		if swapped {
			return Binding{Account: state.Account, Served: state.Served, Found: true}, nil
		}
	}
	return Binding{}, fmt.Errorf("sharedstate: affinity binding for %q is too contended to update", key)
}
