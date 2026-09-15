package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexStreamEventIndicatesProgress(t *testing.T) {
	tests := []struct {
		name     string
		event    string
		progress bool
	}{
		{name: "created", event: `{"type":"response.created"}`, progress: false},
		{name: "rate limits metadata", event: `{"type":"codex.rate_limits"}`, progress: false},
		{name: "empty message announcement", event: `{"type":"response.output_item.added","item":{"type":"message","content":[]}}`, progress: false},
		{name: "empty output text part", event: `{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`, progress: false},
		{name: "empty text delta", event: `{"type":"response.output_text.delta","delta":""}`, progress: false},
		{name: "text delta", event: `{"type":"response.output_text.delta","delta":"hello"}`, progress: true},
		{name: "tool announcement", event: `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call-1"}}`, progress: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexStreamEventIndicatesProgress([]byte(tc.event)); got != tc.progress {
				t.Fatalf("codexStreamEventIndicatesProgress() = %t, want %t", got, tc.progress)
			}
		})
	}
}

func TestCodexExecutorRetriesAfterEmptyMessageAnnouncement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp-1"}}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","content":[]}}` + "\n\n"))
	}))
	defer server.Close()

	result, err := NewCodexExecutor(&config.Config{}).ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": server.URL, "api_key": "test"},
	}, cliproxyexecutor.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`)}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	_, streamErr := drainChunks(result)
	if streamErr == nil {
		t.Fatal("expected incomplete stream error")
	}
	assertNotRequestScopedTestError(t, streamErr)
}
