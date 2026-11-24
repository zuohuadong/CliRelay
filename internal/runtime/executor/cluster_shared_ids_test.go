package executor

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster/sharedstate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	pgredis "github.com/zuohuadong/pgredis-go"
)

// executorSharedIDsNamespace is shared by every store these tests open, so a
// second connection sees what the first one wrote.
const executorSharedIDsNamespace = "executor-shared-ids-test"

// openExecutorSharedStore needs a real server: the cluster-wide identifiers
// live in the cluster database.
func openExecutorSharedStore(t *testing.T) (*sql.DB, *sharedstate.PGStore) {
	t.Helper()
	dsn := os.Getenv("PGREDIS_TEST_DSN")
	if dsn == "" {
		t.Skip("PGREDIS_TEST_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Skipf("postgres unreachable: %v", err)
	}
	store := pgredis.New(db, pgredis.Options{Namespace: executorSharedIDsNamespace, TablePrefix: "clirelay"})
	if err := store.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("EnsureSchema: %v", err)
	}
	return db, sharedstate.NewPGStore(store)
}

func installSharedIDs(t *testing.T) (*sql.DB, *sharedstate.PGStore) {
	t.Helper()
	db, store := openExecutorSharedStore(t)
	SetSharedIDMinter(store)
	resetSyntheticIDCaches()
	t.Cleanup(func() {
		SetSharedIDMinter(nil)
		resetSyntheticIDCaches()
		_ = db.Close()
	})
	return db, store
}

// resetSyntheticIDCaches empties this process's caches, which is what a
// request landing on another node looks like: same shared Redis, cold cache.
func resetSyntheticIDCaches() {
	codexCacheMu.Lock()
	codexCacheMap = make(map[string]codexCache)
	codexCacheMu.Unlock()
	resetUserIDCache()
}

func sharedIDTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: "codex-acct-1", Provider: "codex"}
}

func TestSyntheticCodexSessionIDIsClusterWide(t *testing.T) {
	installSharedIDs(t)
	auth := sharedIDTestAuth()

	first := codexSyntheticSessionID(auth, "gpt-5.5", "first_user:abc")
	resetSyntheticIDCaches() // next turn served by another node
	second := codexSyntheticSessionID(auth, "gpt-5.5", "first_user:abc")
	if first == "" || first != second {
		t.Fatalf("one conversation must get one session id across nodes: %q vs %q", first, second)
	}
	parsed, err := uuid.Parse(first)
	if err != nil || parsed.Version() != 7 {
		t.Fatalf("the synthetic session id must stay a UUIDv7, got %q (%v)", first, err)
	}
	if other := codexSyntheticSessionID(auth, "gpt-5.5", "first_user:other"); other == first {
		t.Fatal("different conversations must not share an id")
	}
}

func TestClaudeToCodexPromptCacheKeyIsClusterWide(t *testing.T) {
	installSharedIDs(t)
	key := codexPromptCacheMapKey(sharedIDTestAuth(), "gpt-5.5", "user_abc_account__session_1")

	first := getOrCreateCodexCacheID(key, codexClaudePromptCacheTTL, uuid.NewString)
	resetSyntheticIDCaches()
	second := getOrCreateCodexCacheID(key, codexClaudePromptCacheTTL, uuid.NewString)
	if first != second {
		t.Fatalf("prompt cache key differs across nodes: %q vs %q", first, second)
	}
	if parsed, err := uuid.Parse(first); err != nil || parsed.Version() != 4 {
		t.Fatalf("the Claude-to-Codex cache key must stay a UUIDv4, got %q (%v)", first, err)
	}
}

func TestCloakedUserIDIsClusterWide(t *testing.T) {
	installSharedIDs(t)
	first := cachedUserID("sk-ant-XXXX-shared")
	resetUserIDCache()
	second := cachedUserID("sk-ant-XXXX-shared")
	if first != second {
		t.Fatalf("cloaked user_id differs across nodes: %q vs %q", first, second)
	}
	if !isValidUserID(first) {
		t.Fatalf("the user_id must keep the Claude Code shape, got %q", first)
	}
}

func TestAdoptedCodexIDExpiresWithTheSharedCopy(t *testing.T) {
	_, store := installSharedIDs(t)
	auth := sharedIDTestAuth()
	key := codexPromptCacheMapKey(auth, "gpt-5.5", "seed")
	ttl := 5 * time.Minute
	seeded, err := store.MintID(context.Background(), sharedIDNamespaceCodex, key, uuid.NewString(), ttl, false)
	if err != nil || seeded.ID == "" {
		t.Fatalf("seed: %+v %v", seeded, err)
	}
	resetSyntheticIDCaches()
	if got := codexSyntheticSessionID(auth, "gpt-5.5", "seed"); got != seeded.ID {
		t.Fatalf("adopted id = %q, want %q", got, seeded.ID)
	}
	codexCacheMu.RLock()
	entry := codexCacheMap[key]
	codexCacheMu.RUnlock()
	if left := time.Until(entry.Expire); left > ttl || left < ttl-30*time.Second || !entry.Shared {
		t.Fatalf("a node adopting an id must expire it with the shared copy (~%s left), got %s shared=%v", ttl, left, entry.Shared)
	}
}

func TestSyntheticIDsFallBackToLocalMintingAndReconcile(t *testing.T) {
	db, _ := installSharedIDs(t)
	auth := sharedIDTestAuth()
	key := codexPromptCacheMapKey(auth, "gpt-5.5", "seed")

	// The cluster database goes away: minting falls back to this process.
	_ = db.Close()
	local := codexSyntheticSessionID(auth, "gpt-5.5", "seed")
	if parsed, err := uuid.Parse(local); err != nil || parsed.Version() != 7 {
		t.Fatalf("local fallback must keep the format, got %q", local)
	}
	if again := codexSyntheticSessionID(auth, "gpt-5.5", "seed"); again != local {
		t.Fatal("the local cache still keeps the conversation stable while the database is down")
	}

	// Meanwhile another node minted the cluster's id for the same
	// conversation; a new connection and store stand in for recovery.
	_, recovered := openExecutorSharedStore(t)
	SetSharedIDMinter(recovered)
	other, err := recovered.MintID(bg(), sharedIDNamespaceCodex, key, uuid.NewString(), time.Hour, false)
	if err != nil || other.ID == local {
		t.Fatalf("setup: %+v %v", other, err)
	}
	if got := codexSyntheticSessionID(auth, "gpt-5.5", "seed"); got != other.ID {
		t.Fatalf("after recovery the node must adopt the cluster's id, got %q want %q", got, other.ID)
	}
}

func TestUserIDRefreshAdoptsTheClusterValue(t *testing.T) {
	_, store := installSharedIDs(t)
	key := userIDCacheKey("sk-ant-XXXX-refresh")
	clusterValue := generateFakeUserID()
	if _, err := store.MintID(bg(), sharedIDNamespaceClaudeUser, key, clusterValue, userIDTTL, true); err != nil {
		t.Fatal(err)
	}
	localValue := generateFakeUserID()
	userIDCacheMu.Lock()
	userIDCache[key] = userIDCacheEntry{value: localValue, expire: time.Now().Add(time.Hour)}
	userIDCacheMu.Unlock()

	refreshSharedUserID(key, localValue)
	userIDCacheMu.RLock()
	got := userIDCache[key].value
	userIDCacheMu.RUnlock()
	if got != clusterValue {
		t.Fatalf("refresh must adopt the value the cluster already holds, got %q", got)
	}
}

func TestSharedIDsNotInstalledKeepLocalBehaviour(t *testing.T) {
	SetSharedIDMinter(nil)
	resetSyntheticIDCaches()
	t.Cleanup(resetSyntheticIDCaches)
	auth := sharedIDTestAuth()
	first := codexSyntheticSessionID(auth, "gpt-5.5", "seed")
	if first != codexSyntheticSessionID(auth, "gpt-5.5", "seed") {
		t.Fatal("per-process cache must keep one id per conversation")
	}
	codexCacheMu.RLock()
	shared := codexCacheMap[codexPromptCacheMapKey(auth, "gpt-5.5", "seed")].Shared
	codexCacheMu.RUnlock()
	if shared {
		t.Fatal("outside a cluster nothing is marked shared")
	}
}

func bg() context.Context { return context.Background() }
