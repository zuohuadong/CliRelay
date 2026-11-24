package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster/sharedstate"
)

// fakeClusterQuota stands in for the shared counters: one instance is what
// every node of the test cluster sees. It mirrors the contract the PostgreSQL
// adapter implements — admit counts, slots, batched counts and dashboard
// rates — without a database.
type fakeClusterQuota struct {
	mu        sync.Mutex
	available bool
	nodes     int
	rpm       map[string]float64
	tpm       map[string]float64
	slots     map[string]int
	pending   map[string]float64
}

func newFakeClusterQuota(nodes int) *fakeClusterQuota {
	return &fakeClusterQuota{
		available: true,
		nodes:     nodes,
		rpm:       make(map[string]float64),
		tpm:       make(map[string]float64),
		slots:     make(map[string]int),
		pending:   make(map[string]float64),
	}
}

func (q *fakeClusterQuota) setAvailable(available bool) {
	q.mu.Lock()
	q.available = available
	q.mu.Unlock()
}

func (q *fakeClusterQuota) Available() bool { return q.available }
func (q *fakeClusterQuota) ActiveNodes() int {
	return q.nodes
}

func (q *fakeClusterQuota) AdmitRequest(ctx context.Context, subject string, concurrencyLimit int) (sharedstate.Admission, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.available {
		return sharedstate.Admission{}, sharedstate.ErrUnavailable
	}
	q.rpm[subject]++
	adm := sharedstate.Admission{RPM: q.rpm[subject], TPM: q.tpm[subject], Release: func() {}}
	if concurrencyLimit > 0 {
		if q.slots[subject] < concurrencyLimit {
			q.slots[subject]++
			adm.SlotTaken = true
			adm.Release = func() { q.releaseSlot(subject) }
		}
		adm.Held = q.slots[subject]
	}
	return adm, nil
}

func (q *fakeClusterQuota) releaseSlot(subject string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.slots[subject] > 0 {
		q.slots[subject]--
	}
}

func (q *fakeClusterQuota) CountRequest(subject string) {
	q.mu.Lock()
	q.pending[subject]++
	q.mu.Unlock()
}

func (q *fakeClusterQuota) AddTokens(subject string, tokens int64) {
	q.mu.Lock()
	q.tpm[subject] += float64(tokens)
	q.mu.Unlock()
}

// Flush writes the batched counts, as the shared store's periodic flush does.
func (q *fakeClusterQuota) Flush() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for subject, requests := range q.pending {
		q.rpm[subject] += requests
	}
	q.pending = make(map[string]float64)
}

func (q *fakeClusterQuota) Rates(ctx context.Context, subjects []string) (map[string]sharedstate.Rate, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.available {
		return nil, sharedstate.ErrUnavailable
	}
	out := make(map[string]sharedstate.Rate, len(subjects))
	for _, subject := range subjects {
		out[subject] = sharedstate.Rate{RPM: q.rpm[subject], TPM: q.tpm[subject]}
	}
	return out, nil
}

func bg() context.Context { return context.Background() }

func installFakeClusterQuota(t *testing.T, nodes int) *fakeClusterQuota {
	t.Helper()
	q := newFakeClusterQuota(nodes)
	SetClusterQuota(q)
	t.Cleanup(func() { SetClusterQuota(nil) })
	return q
}

func quotaRouter(metadata map[string]string, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("apiKey", "sk-XXXX-cluster-test")
		c.Set("accessMetadata", metadata)
		c.Next()
	})
	router.Use(QuotaMiddleware())
	if handler == nil {
		handler = func(c *gin.Context) { c.Status(http.StatusNoContent) }
	}
	router.POST("/v1/chat/completions", handler)
	return router
}

func postQuota(router *gin.Engine) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newQuotaPostRequest("sk-XXXX-cluster-test"))
	return rec
}

func TestClusterRPMLimitIsGlobal(t *testing.T) {
	resetQuotaMiddlewareState(t)
	cluster := installFakeClusterQuota(t, 2)
	router := quotaRouter(map[string]string{"rpm-limit": "4"}, nil)

	// Another node already served three requests of this key this minute.
	for i := 0; i < 3; i++ {
		if _, err := cluster.AdmitRequest(bg(), "sk-XXXX-cluster-test", 0); err != nil {
			t.Fatal(err)
		}
	}
	if rec := postQuota(router); rec.Code != http.StatusNoContent {
		t.Fatalf("4th request cluster-wide must pass, got %d", rec.Code)
	}
	rec := postQuota(router)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("X-CliRelay-Quota-Code") != "rpm_limit_exceeded" {
		t.Fatalf("5th request cluster-wide must be refused, got %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-CliRelay-Quota-Limit"); got != "4" {
		t.Fatalf("shared mode enforces the configured limit, header limit = %q", got)
	}
}

func TestClusterConcurrencyLimitIsGlobal(t *testing.T) {
	resetQuotaMiddlewareState(t)
	cluster := installFakeClusterQuota(t, 2)
	router := quotaRouter(map[string]string{"concurrency-limit": "1"}, nil)

	held, err := cluster.AdmitRequest(bg(), "sk-XXXX-cluster-test", 1)
	if err != nil || !held.SlotTaken {
		t.Fatalf("peer slot: %+v %v", held, err)
	}
	rec := postQuota(router)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("X-CliRelay-Quota-Code") != "concurrency_limit_exceeded" {
		t.Fatalf("slot held by another node must refuse this one, got %d %s", rec.Code, rec.Body.String())
	}
	held.Release()
	if rec = postQuota(router); rec.Code != http.StatusNoContent {
		t.Fatalf("slot released by the other node must be usable, got %d %s", rec.Code, rec.Body.String())
	}
	if n := keyConcurrencyCount("sk-XXXX-cluster-test"); n != 0 {
		t.Fatalf("local mirror of the slot must be released with the request, got %d", n)
	}
}

// TestClusterFallsBackToNodeShareAndRecovers covers the degraded mode: with the
// shared counters gone each node enforces ceil(limit/nodes) on its own, and
// once they are back the configured limit applies cluster-wide again.
func TestClusterFallsBackToNodeShareAndRecovers(t *testing.T) {
	resetQuotaMiddlewareState(t)
	cluster := installFakeClusterQuota(t, 2)
	router := quotaRouter(map[string]string{"rpm-limit": "4"}, nil)

	cluster.setAvailable(false)
	if rec := postQuota(router); rec.Code != http.StatusNoContent {
		t.Fatalf("request during the outage = %d", rec.Code)
	}
	if rec := postQuota(router); rec.Code != http.StatusNoContent {
		t.Fatalf("second request within this node's share (2) = %d", rec.Code)
	}
	rec := postQuota(router)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request exceeds this node's share of 4/2, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-CliRelay-Quota-Limit"); got != "2" {
		t.Fatalf("degraded mode enforces the node share, header limit = %q", got)
	}

	// The shared counters come back with empty windows; the full limit applies.
	cluster.setAvailable(true)
	for i := 0; i < 4; i++ {
		if rec := postQuota(router); rec.Code != http.StatusNoContent {
			t.Fatalf("request %d after recovery = %d", i+1, rec.Code)
		}
	}
	if rec := postQuota(router); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("fifth request after recovery must be refused, got %d", rec.Code)
	}
}

func TestClusterWithoutSharedStateSplitsConcurrency(t *testing.T) {
	resetQuotaMiddlewareState(t)
	cluster := installFakeClusterQuota(t, 3)
	cluster.setAvailable(false)

	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	router := quotaRouter(map[string]string{"concurrency-limit": "5"}, func(c *gin.Context) {
		entered <- struct{}{}
		<-release
		c.Status(http.StatusNoContent)
	})
	done := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- postQuota(router).Code }()
		<-entered
	}
	// ceil(5/3) = 2 slots on this node.
	if rec := postQuota(router); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third concurrent request exceeds the node share, got %d", rec.Code)
	}
	close(release)
	for i := 0; i < 2; i++ {
		if code := <-done; code != http.StatusNoContent {
			t.Fatalf("held request finished with %d", code)
		}
	}
}

func TestClusterTokensAndDashboardAreShared(t *testing.T) {
	resetQuotaMiddlewareState(t)
	cluster := installFakeClusterQuota(t, 2)
	router := quotaRouter(map[string]string{"tpm-limit": "1000"}, nil)

	if rec := postQuota(router); rec.Code != http.StatusNoContent {
		t.Fatalf("first request = %d", rec.Code)
	}
	RecordTokenUsage("sk-XXXX-cluster-test", 600)
	// Tokens used on the other node count too.
	cluster.AddTokens("sk-XXXX-cluster-test", 500)
	rec := postQuota(router)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("X-CliRelay-Quota-Code") != "tpm_limit_exceeded" {
		t.Fatalf("1100 tokens across the cluster must exceed 1000, got %d %s", rec.Code, rec.Body.String())
	}

	for i := 0; i < 5; i++ {
		if _, err := cluster.AdmitRequest(bg(), "sk-XXXX-cluster-test", 0); err != nil {
			t.Fatal(err)
		}
	}
	snaps, total := GetConcurrencySnapshot()
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %+v", snaps)
	}
	// Two admitted requests plus five from the other node.
	if snaps[0].RPM != 7 || snaps[0].TPM != 1100 || total != 7 {
		t.Fatalf("dashboard must show cluster-wide usage, got %+v total %d", snaps[0], total)
	}
}

func TestClusterUnlimitedKeysAreCountedWithoutWaiting(t *testing.T) {
	resetQuotaMiddlewareState(t)
	cluster := installFakeClusterQuota(t, 2)
	router := quotaRouter(map[string]string{"daily-limit": "100"}, nil)

	for i := 0; i < 3; i++ {
		if rec := postQuota(router); rec.Code != http.StatusNoContent {
			t.Fatalf("request %d = %d", i, rec.Code)
		}
	}
	rates, err := cluster.Rates(bg(), []string{"sk-XXXX-cluster-test"})
	if err != nil {
		t.Fatal(err)
	}
	if rates["sk-XXXX-cluster-test"].RPM != 0 {
		t.Fatalf("unlimited keys are batched, not written per request: %+v", rates)
	}
	cluster.Flush()
	rates, _ = cluster.Rates(bg(), []string{"sk-XXXX-cluster-test"})
	if rates["sk-XXXX-cluster-test"].RPM != 3 {
		t.Fatalf("after the flush the count is shared: %+v", rates)
	}
}

func TestSplitLimit(t *testing.T) {
	cases := []struct{ limit, nodes, want int }{
		{0, 3, 0}, {10, 1, 10}, {10, 2, 5}, {5, 3, 2}, {1, 4, 1}, {7, 0, 7},
	}
	for _, tc := range cases {
		if got := splitLimit(tc.limit, tc.nodes); got != tc.want {
			t.Errorf("splitLimit(%d, %d) = %d, want %d", tc.limit, tc.nodes, got, tc.want)
		}
	}
}
