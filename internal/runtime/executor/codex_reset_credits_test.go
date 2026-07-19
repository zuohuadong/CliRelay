package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexExecutorListRateLimitResetCredits(t *testing.T) {
	auth := codexResetTestAuth()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", req.Method)
		}
		if req.URL.String() != codexResetCreditsURL {
			t.Fatalf("url = %s, want %s", req.URL.String(), codexResetCreditsURL)
		}
		assertCodexResetHeaders(t, req)
		return codexResetResponse(http.StatusOK, `{"available_count":1,"credits":[{"id":"credit-1","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-07-01T00:00:00Z","expires_at":null,"title":"One reset","description":null}]}`), nil
	}))

	credits, err := NewCodexExecutor(&config.Config{}).ListRateLimitResetCredits(ctx, auth)
	if err != nil {
		t.Fatalf("ListRateLimitResetCredits() error = %v", err)
	}
	if credits.AvailableCount != 1 || len(credits.Credits) != 1 {
		t.Fatalf("credits = %#v, want one available credit", credits)
	}
	if credits.Credits[0].ID != "credit-1" || credits.Credits[0].Status != "available" {
		t.Fatalf("credit = %#v, want credit-1 available", credits.Credits[0])
	}
}

func TestCodexExecutorConsumeRateLimitResetCredit(t *testing.T) {
	auth := codexResetTestAuth()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", req.Method)
		}
		if req.URL.String() != codexResetCreditsConsumeURL {
			t.Fatalf("url = %s, want %s", req.URL.String(), codexResetCreditsConsumeURL)
		}
		assertCodexResetHeaders(t, req)
		var payload codexRateLimitResetRequest
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if payload.CreditID != "credit-1" || payload.RedeemRequestID != "request-1" {
			t.Fatalf("payload = %#v, want selected credit and request id", payload)
		}
		return codexResetResponse(http.StatusOK, `{"code":"reset","windows_reset":2}`), nil
	}))

	result, err := NewCodexExecutor(&config.Config{}).ConsumeRateLimitResetCredit(
		ctx,
		auth,
		"credit-1",
		"request-1",
	)
	if err != nil {
		t.Fatalf("ConsumeRateLimitResetCredit() error = %v", err)
	}
	if result.Code != "reset" || result.WindowsReset != 2 {
		t.Fatalf("result = %#v, want reset with two windows", result)
	}
}

func TestCodexExecutorResetCreditsPreservesUpstreamStatus(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return codexResetResponse(http.StatusNotFound, `{"error":{"message":"not available"}}`), nil
	}))

	_, err := NewCodexExecutor(&config.Config{}).ListRateLimitResetCredits(ctx, codexResetTestAuth())
	if err == nil {
		t.Fatal("ListRateLimitResetCredits() error = nil, want upstream error")
	}
	statusError, ok := err.(interface{ StatusCode() int })
	if !ok || statusError.StatusCode() != http.StatusNotFound {
		t.Fatalf("error = %T %v, want status 404", err, err)
	}
}

func TestCodexExecutorResetCreditsRejectsCodexAPIKeyAuth(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("upstream request must not run for API-key auth")
		return nil, nil
	}))
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			cliproxyauth.AttributeAPIKey: "sk-test",
		},
	}

	_, err := NewCodexExecutor(&config.Config{}).ListRateLimitResetCredits(ctx, auth)
	if err == nil {
		t.Fatal("ListRateLimitResetCredits() error = nil, want OAuth validation error")
	}
	statusError, ok := err.(interface{ StatusCode() int })
	if !ok || statusError.StatusCode() != http.StatusBadRequest {
		t.Fatalf("error = %T %v, want status 400", err, err)
	}
}

func TestDecodeCodexRateLimitResetCreditsRejectsMalformedResponses(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"credits":[]}`,
		`{"available_count":1,"credits":[{"id":"credit-1"}]}`,
	} {
		if _, err := decodeCodexRateLimitResetCredits([]byte(body)); err == nil {
			t.Fatalf("decodeCodexRateLimitResetCredits(%s) error = nil, want validation error", body)
		}
	}
}

func TestDecodeCodexRateLimitResetResultRejectsMalformedResponses(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"code":"reset"}`,
		`{"code":"unexpected","windows_reset":0}`,
	} {
		if _, err := decodeCodexRateLimitResetResult([]byte(body)); err == nil {
			t.Fatalf("decodeCodexRateLimitResetResult(%s) error = nil, want validation error", body)
		}
	}
}

func codexResetTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"header:Authorization":      "Bearer attacker-token",
			"header:Chatgpt-Account-Id": "acct-attacker",
			"header:Content-Type":       "text/plain",
			"header:Accept":             "text/plain",
			"header:Host":               "attacker.invalid",
		},
		Metadata: map[string]any{
			"access_token": "test-token",
			"account_id":   "acct-123",
		},
	}
}

func assertCodexResetHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want bearer token", got)
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "acct-123" {
		t.Fatalf("Chatgpt-Account-Id = %q, want acct-123", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
	if req.Host != req.URL.Host {
		t.Fatalf("Host = %q, want selected upstream host %q", req.Host, req.URL.Host)
	}
}

func codexResetResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
