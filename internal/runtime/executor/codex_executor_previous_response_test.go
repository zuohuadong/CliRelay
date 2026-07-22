package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorPreservesPreviousResponseIDUpstream(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		execute func(*CodexExecutor, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) error
	}{
		{
			name: "execute",
			execute: func(executor *CodexExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, err := executor.Execute(context.Background(), auth, req, opts)
				return err
			},
		},
		{
			name: "execute stream",
			execute: func(executor *CodexExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					return err
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						return chunk.Err
					}
				}
				return nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Fatalf("read request body: %v", errRead)
				}
				gotBody = body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_next\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
			}))
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			}}
			req := cliproxyexecutor.Request{
				Model:   "gpt-5.6-sol",
				Payload: []byte(`{"model":"gpt-5.6-sol","previous_response_id":"resp_original","input":[{"role":"user","content":"fix"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FromString("openai-response"),
				ResponseFormat:  sdktranslator.FromString("openai-response"),
				OriginalRequest: req.Payload,
				Stream:          tc.name == "execute stream",
			}

			if err := tc.execute(executor, auth, req, opts); err != nil {
				t.Fatalf("execute request: %v", err)
			}
			if got := gjson.GetBytes(gotBody, "previous_response_id").String(); got != "resp_original" {
				t.Fatalf("upstream previous_response_id = %q, want resp_original; body=%s", got, gotBody)
			}
		})
	}
}
