package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestRecordAPIResponseMetadataStoresHeadersWhenRequestLogDisabled(t *testing.T) {
	ctx := logging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	headers.Add("X-Upstream-Request-Id", "upstream-req-1")

	RecordAPIResponseMetadata(ctx, &config.Config{}, http.StatusOK, headers)
	headers.Set("X-Upstream-Request-Id", "mutated")

	got := logging.GetResponseHeaders(ctx)
	if got.Get("X-Upstream-Request-Id") != "upstream-req-1" {
		t.Fatalf("response header = %q, want %q", got.Get("X-Upstream-Request-Id"), "upstream-req-1")
	}
}

func TestAppendAPIWebsocketResponseCapsTimeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", c)
	cfg := &config.Config{}
	cfg.RequestLog = true

	largePayload := []byte(`{"type":"response.output_text.delta","delta":"` + strings.Repeat("x", apiWebsocketTimelineChunkMaxBytes*2) + `"}`)
	for i := 0; i < 40; i++ {
		AppendAPIWebsocketResponse(ctx, cfg, largePayload)
	}

	value, exists := c.Get(apiWebsocketTimelineKey)
	if !exists {
		t.Fatal("expected websocket timeline to be recorded")
	}
	timeline, ok := value.([]byte)
	if !ok {
		t.Fatalf("timeline type = %T, want []byte", value)
	}
	if len(timeline) > apiWebsocketTimelineMaxBytes {
		t.Fatalf("timeline length = %d, want <= %d", len(timeline), apiWebsocketTimelineMaxBytes)
	}
	if !strings.Contains(string(timeline), "api websocket payload truncated") {
		t.Fatal("expected truncated payload marker")
	}
}
