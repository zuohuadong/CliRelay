package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/diagnostics"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

type stubStreamingLogWriter struct{}

func (stubStreamingLogWriter) WriteChunkAsync([]byte) {}
func (stubStreamingLogWriter) NoteDroppedChunks(int) {}
func (stubStreamingLogWriter) WriteStatus(int, map[string][]string) error { return nil }
func (stubStreamingLogWriter) WriteAPIRequest([]byte) error { return nil }
func (stubStreamingLogWriter) WriteAPIResponse([]byte) error { return nil }
func (stubStreamingLogWriter) WriteAPIWebsocketTimeline([]byte) error { return nil }
func (stubStreamingLogWriter) WriteAPIResponseErrors([]*interfaces.ErrorMessage) error { return nil }
func (stubStreamingLogWriter) SetFirstChunkTimestamp(time.Time) {}
func (stubStreamingLogWriter) Close() error { return nil }

type stubRequestLogger struct{}

func (stubRequestLogger) LogRequest(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, string, time.Time, time.Time) error {
	return nil
}
func (stubRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (logging.StreamingLogWriter, error) {
	return stubStreamingLogWriter{}, nil
}
func (stubRequestLogger) IsEnabled() bool { return true }

// The middleware lists a gateway request in flight while it is served and
// takes it off however it ends, a panicking handler included.
func TestRequestLoggingMiddlewareListsTheRequestInFlightUntilItEnds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const tenant = "tenant-middleware-inflight"
	inTenant := func(id string) bool { return id == tenant }
	listedWhileServed := make(chan int, 1)

	r := gin.New()
	r.Use(gin.CustomRecovery(func(c *gin.Context, _ any) { c.AbortWithStatus(http.StatusInternalServerError) }))
	r.Use(RequestLoggingMiddleware(stubRequestLogger{}))
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		diagnostics.SetAuth(c, "openai", "sk-test", "key-1", "test key")
		diagnostics.SetTenant(c, tenant)
		requests, _ := diagnostics.InFlight(inTenant, time.Now())
		listedWhileServed <- len(requests)
		if c.Query("panic") != "" {
			panic("handler failed")
		}
		c.Status(http.StatusNoContent)
	})

	for _, target := range []string{"/v1/chat/completions", "/v1/chat/completions?panic=1"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, target, nil))
		if n := <-listedWhileServed; n != 1 {
			t.Fatalf("%s: %d requests in flight while served, want 1", target, n)
		}
		if requests, _ := diagnostics.InFlight(inTenant, time.Now()); len(requests) != 0 {
			t.Fatalf("%s: still in flight after it ended: %+v", target, requests)
		}
	}
}
