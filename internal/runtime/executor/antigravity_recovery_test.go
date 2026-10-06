package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityRecovery(t *testing.T) {
	const success = `data: {"response":{
data: "candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},"finishReason":"STOP"}],
data: "usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}}

`
	tests := []struct {
		name string
		body string
		code int
	}{
		{name: "multiline", body: success},
		{name: "backend error", body: `data: {"error":{"code":429,"message":"limited","status":"RESOURCE_EXHAUSTED"}}` + "\n\n", code: 429},
		{name: "invalid backend status", body: `data: {"error":{"code":200,"message":"backend failed"}}` + "\n\n", code: 502},
		{name: "truncated JSON", body: "data: {\"response\":\n", code: 502},
		{name: "truncated before done", body: "data: {\"response\":\ndata: [DONE]\n", code: 502},
		{name: "trailing error", body: success + "data: {\"error\":{\"code\":503,\"message\":\"unavailable\"}}\n\n", code: 503},
	}
	for _, model := range []string{"gemini-3.7-flash", "gemini-3.1-pro", "claude-sonnet-4-6"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range tests {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", model, stream, tc.name), func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != antigravityStreamPath || r.URL.Query().Get("alt") != "sse" {
							t.Errorf("unexpected upstream URL: %s", r.URL)
							http.Error(w, "wrong endpoint", http.StatusNotFound)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, tc.body)
					}))
					defer server.Close()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					auth := testAntigravityAuth(server.URL)
					auth.Metadata["project_id"] = "project-1"
					executor := NewAntigravityExecutor(&config.Config{})
					req := cliproxyexecutor.Request{
						Model:   model,
						Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
					}
					opts := cliproxyexecutor.Options{
						SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, Stream: stream,
					}
					var resultErr error
					var output strings.Builder
					if stream {
						result, err := executor.ExecuteStream(ctx, auth, req, opts)
						resultErr = err
						if err == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									resultErr = chunk.Err
								} else {
									if resultErr != nil {
										t.Fatal("received success payload after terminal error")
									}
									output.Write(chunk.Payload)
								}
							}
						}
					} else {
						result, err := executor.Execute(ctx, auth, req, opts)
						resultErr = err
						output.Write(result.Payload)
						if tc.code == 0 && gjson.GetBytes(result.Payload, "candidates.0.content.parts.0.text").String() != "recovered" {
							t.Fatalf("unexpected non-stream response: %s", result.Payload)
						}
					}
					if tc.code == 0 {
						if resultErr != nil || !strings.Contains(output.String(), "recovered") {
							t.Fatalf("result: %q, error: %v", output.String(), resultErr)
						}
					} else {
						status, ok := resultErr.(interface{ StatusCode() int })
						if !ok || status.StatusCode() != tc.code {
							t.Fatalf("error = %v, want status %d", resultErr, tc.code)
						}
					}
				})
			}
		}
	}
}
