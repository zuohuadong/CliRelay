package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

func TestCollectSystemStatsCachesSlowUsageMetrics(t *testing.T) {
	now := time.Unix(2_000, 0)
	loadCalls := 0
	h := &Handler{
		cfg: &config.Config{},
		channelLatencyLoader: func(_ context.Context, days int) ([]usage.ChannelLatency, error) {
			loadCalls++
			return []usage.ChannelLatency{{Source: "codex", Count: int64(days), AvgMs: 123}}, nil
		},
		usageAggregateCacheTTL: time.Minute,
		usageAggregateCacheNow: func() time.Time {
			return now
		},
	}

	first := h.collectSystemStats()
	second := h.collectSystemStats()
	if loadCalls != 1 {
		t.Fatalf("channel latency loader calls = %d, want 1", loadCalls)
	}
	if len(first.ChannelLatency) != 1 || len(second.ChannelLatency) != 1 {
		t.Fatalf("cached channel latency missing: first=%#v second=%#v", first.ChannelLatency, second.ChannelLatency)
	}

	now = now.Add(2 * time.Minute)
	_ = h.collectSystemStats()
	if loadCalls != 2 {
		t.Fatalf("expired slow stats cache loader calls = %d, want 2", loadCalls)
	}
}

func TestGetSystemStatsCancellationDoesNotCacheSlowMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	started := make(chan struct{})
	var loadCalls atomic.Int32
	h := &Handler{
		cfg: &config.Config{},
		channelLatencyLoader: func(ctx context.Context, _ int) ([]usage.ChannelLatency, error) {
			loadCalls.Add(1)
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		usageAggregateCacheTTL: time.Minute,
	}

	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	requestContext, cancel := context.WithCancel(context.Background())
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/system-stats", nil).WithContext(requestContext)
	done := make(chan struct{})
	go func() {
		h.GetSystemStats(ginContext)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("system stats request did not stop after cancellation")
	}

	h.channelLatencyLoader = func(context.Context, int) ([]usage.ChannelLatency, error) {
		loadCalls.Add(1)
		return []usage.ChannelLatency{{Source: "codex", Count: 1, AvgMs: 42}}, nil
	}
	stats := h.collectSystemStats()
	if got := loadCalls.Load(); got != 2 {
		t.Fatalf("canceled slow metrics were cached: loader calls = %d, want 2", got)
	}
	if len(stats.ChannelLatency) != 1 {
		t.Fatalf("fresh slow metrics missing after cancellation: %#v", stats.ChannelLatency)
	}
}
