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

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature/signaturetest"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

func TestAntigravityRecoveryCompatibility(t *testing.T) {
	const success = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}}` + "\n\n"
	const malformed = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"planning"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}` + "\n\n"
	const wall = `{"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens allowed (1048576)."}}`
	for _, streaming := range []bool{false, true} {
		for _, scenario := range []struct {
			name  string
			codes []int
			body  []string
			want  int
		}{
			{"session rotation then malformed call", []int{400, 200, 200}, []string{wall, malformed, success}, 0},
			{"session rotation limit", []int{400, 400, 400}, []string{wall, wall, wall}, 400},
			{"malformed call limit", []int{200, 200, 200}, []string{malformed, malformed, malformed}, 502},
			{"unrelated invalid argument", []int{400}, []string{`{"error":{"message":"invalid tool schema"}}`}, 400},
			{"malformed after visible output", []int{200}, []string{success + malformed}, 502},
			{"malformed with visible tool", []int{200}, []string{`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"run","args":{}}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}` + "\n\n"}, 502},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", streaming, scenario.name), func(t *testing.T) {
				var sessions []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if got := gjson.GetBytes(body, "request.contents.0.parts.0.text").String(); got != "hello" {
						t.Errorf("conversation changed: %s", body)
					}
					if got := gjson.GetBytes(body, "request.toolConfig.functionCallingConfig.mode").String(); got != "VALIDATED" {
						t.Errorf("missing function-calling configuration: %s", body)
					}
					sessions = append(sessions, gjson.GetBytes(body, "request.sessionId").String())
					index := len(sessions) - 1
					if index >= len(scenario.codes) {
						t.Error("recovery exceeded request budget")
						w.WriteHeader(500)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(scenario.codes[index])
					_, _ = io.WriteString(w, scenario.body[index])
				}))
				defer server.Close()
				auth := testAntigravityAuth(server.URL)
				auth.Metadata["project_id"] = "project-1"
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				executor := NewAntigravityExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"sessionId":"original-session"}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, Stream: streaming}
				var resultErr error
				var output strings.Builder
				if streaming {
					result, err := executor.ExecuteStream(ctx, auth, req, opts)
					resultErr = err
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								resultErr = chunk.Err
							} else {
								if resultErr != nil {
									t.Fatal("completion emitted after terminal error")
								}
								output.Write(chunk.Payload)
							}
						}
					}
				} else {
					result, err := executor.Execute(ctx, auth, req, opts)
					resultErr = err
					output.Write(result.Payload)
				}
				if len(sessions) != len(scenario.codes) {
					t.Fatalf("requests = %d, want %d", len(sessions), len(scenario.codes))
				}
				if scenario.want == 0 {
					if resultErr != nil || !strings.Contains(output.String(), "recovered") {
						t.Fatalf("result = %s, error = %v", output.String(), resultErr)
					}
					if sessions[0] == "" || sessions[0] == sessions[1] || sessions[1] != sessions[2] {
						t.Fatalf("rotated session must survive malformed-call retry: %v", sessions)
					}
				} else if code, ok := resultErr.(interface{ StatusCode() int }); !ok || code.StatusCode() != scenario.want {
					t.Fatalf("error = %v, want status %d", resultErr, scenario.want)
				}
			})
		}
	}

	t.Run("mixed tools and explicit policy", func(t *testing.T) {
		for _, mode := range []string{"AUTO", "ANY", "NONE", "VALIDATED"} {
			payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"tools":[{"googleSearch":{}},{"codeExecution":{}},{"functionDeclarations":[{"name":"run","parameters":{"type":"object","properties":{"title":{"type":"string"}}}}],"google_search":{}}],"toolConfig":{"includeServerSideToolInvocations":true,"functionCallingConfig":{"mode":"` + mode + `"}}}}`)
			auth := testAntigravityAuth("https://example.com")
			auth.Metadata["project_id"] = "project-1"
			req, err := NewAntigravityExecutor(&config.Config{}).buildRequest(context.Background(), auth, "token", "gemini-3.7-flash", payload, true, "", "")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(req.Body)
			if len(gjson.GetBytes(body, "request.tools").Array()) != 1 ||
				gjson.GetBytes(body, "request.tools.0.functionDeclarations.0.name").String() != "run" ||
				gjson.GetBytes(body, "request.toolConfig.functionCallingConfig.mode").String() != mode ||
				gjson.GetBytes(body, "request.toolConfig.includeServerSideToolInvocations").Exists() ||
				gjson.GetBytes(body, "request.tools.0.google_search").Exists() {
				t.Fatalf("incompatible tools or policy changed: %s", body)
			}
			if got := gjson.GetBytes(body, "request.tools.0.functionDeclarations.0.parameters.properties.title.type").String(); got != "string" {
				t.Fatalf("tool schema changed: %s", body)
			}
		}
		searchOnly := []byte(`{"request":{"tools":[{"googleSearch":{}}]}}`)
		if !gjson.GetBytes(normalizeAntigravityToolConfig(searchOnly), "request.tools.0.googleSearch").Exists() {
			t.Fatal("built-in-only search was dropped")
		}
	})

	t.Run("current Hub identity and rotated signature channel", func(t *testing.T) {
		if ua := misc.AntigravityUserAgent(); !strings.Contains(ua, "aidev_client; os_type=darwin; arch=arm64") {
			t.Fatalf("Hub request fingerprint = %q", ua)
		}
		if antigravityLoadCodeAssistBaseURL(nil) != antigravityBaseURLDaily {
			t.Fatal("project discovery and generation use different endpoint tiers")
		}
		if _, err := internalsignature.InspectAntigravityClaudeCAQSSignature(signaturetest.AntigravityCAQSWithChannel(19)); err != nil {
			t.Fatalf("rotated channel rejected: %v", err)
		}
	})

	t.Run("two-turn replay honors final user rules", func(t *testing.T) {
		conversation := newIssue6825Conversation(t, issue6825ToolResponse(issue4959GeminiThoughtSignature(), ""))
		opts := cliproxyexecutor.Options{Headers: http.Header{"Session-Id": {t.Name()}}}
		assistant, _ := conversation.call([]byte(issue6825FirstRequest), opts, false, nil)
		conversation.executor.cfg.Payload = config.PayloadConfig{
			Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gemini-*"}}, Params: []string{"tools", "contents.1.parts.0.thoughtSignature"}}},
		}
		followup := issue6825Followup(t, assistant)
		followup, _ = sjson.SetBytes(followup, "tool_choice", "none")
		_, second := conversation.call(followup, opts, false, nil)
		if gjson.GetBytes(second.body, "request.tools").Exists() || gjson.GetBytes(second.body, "request.contents.1.parts.0.thoughtSignature").Exists() {
			t.Fatalf("payload filters were undone: %s", second.body)
		}
	})

	t.Run("non-stream tool-only response without finishReason preserves replay", func(t *testing.T) {
		signature := issue4959GeminiThoughtSignature()
		response := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_time","args":{"tz":"UTC"}},"thoughtSignature":"` + signature + `"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`
		conversation := newIssue6825Conversation(t, response)
		opts := cliproxyexecutor.Options{Headers: http.Header{"Session-Id": {t.Name()}}}
		assistant, _ := conversation.call([]byte(issue6825FirstRequest), opts, false, nil)
		_, second := conversation.call(issue6825Followup(t, assistant), opts, false, nil)
		if got := issue6825FirstToolSignature(t, second.body); got != signature {
			t.Fatalf("historical signature = %q, want %q", got, signature)
		}
	})

	t.Run("missing credentials fail before upstream", func(t *testing.T) {
		_, err := NewAntigravityExecutor(&config.Config{}).Execute(context.Background(), nil,
			cliproxyexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
		if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusUnauthorized {
			t.Fatalf("missing credentials error = %v, want 401", err)
		}
	})

	t.Run("supported entrypoints preserve answers", func(t *testing.T) {
		for _, input := range []struct {
			format  sdktranslator.Format
			payload string
		}{
			{sdktranslator.FormatOpenAI, `{"messages":[{"role":"user","content":"hello"}]}`},
			{sdktranslator.FormatOpenAIResponse, `{"input":[{"role":"user","content":"hello"}]}`},
			{sdktranslator.FormatClaude, `{"messages":[{"role":"user","content":"hello"}],"max_tokens":128}`},
			{sdktranslator.FormatGemini, `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`},
			{sdktranslator.FormatInteractions, `{"input":"hello"}`},
		} {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t", input.format, streaming), func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != antigravityStreamPath {
							t.Errorf("upstream path = %s", r.URL.Path)
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, success)
					}))
					defer server.Close()
					auth := testAntigravityAuth(server.URL)
					auth.Metadata["project_id"] = "project-1"
					executor := NewAntigravityExecutor(&config.Config{})
					req := cliproxyexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(input.payload)}
					opts := cliproxyexecutor.Options{SourceFormat: input.format, Stream: streaming}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					var output strings.Builder
					if streaming {
						result, err := executor.ExecuteStream(ctx, auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							output.Write(chunk.Payload)
						}
					} else {
						result, err := executor.Execute(ctx, auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						output.Write(result.Payload)
					}
					if !strings.Contains(output.String(), "recovered") {
						t.Fatalf("answer lost in translation: %s", output.String())
					}
				})
			}
		}
	})
}
