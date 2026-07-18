package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type countingRoundTripper func(*http.Request) (*http.Response, error)

func (f countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCodexNetworkPathsRequireManagedEgressBeforeNetwork(t *testing.T) {
	originalTransport := http.DefaultTransport
	var networkCalls atomic.Int32
	http.DefaultTransport = countingRoundTripper(func(*http.Request) (*http.Response, error) {
		networkCalls.Add(1)
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"error":"unexpected network"}`)),
			Header:     make(http.Header),
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	auth := &coreauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"refresh_token": "must-not-leave-host"},
	}
	if _, _, err := ensureAccessToken(context.Background(), nil, auth); !errors.Is(err, egress.ErrEgressRequired) {
		t.Fatalf("ensureAccessToken() error = %v, want ErrEgressRequired", err)
	}
	if _, _, err := fetchModels(context.Background(), auth, "access-token", defaultClientVersion); !errors.Is(err, egress.ErrEgressRequired) {
		t.Fatalf("fetchModels() error = %v, want ErrEgressRequired", err)
	}
	if got := networkCalls.Load(); got != 0 {
		t.Fatalf("network calls = %d, want 0", got)
	}
}

func TestCodexModelsURL(t *testing.T) {
	got, err := codexModelsURL(" 0.144.1 ")
	if err != nil {
		t.Fatalf("codexModelsURL: %v", err)
	}
	want := "https://chatgpt.com/backend-api/codex/models?client_version=0.144.1"
	if got != want {
		t.Fatalf("codexModelsURL = %q, want %q", got, want)
	}
}

func TestCountModels(t *testing.T) {
	count, err := countModels([]byte(`{"models":[{"slug":"a"},{"slug":"b"}]}`))
	if err != nil {
		t.Fatalf("countModels(valid): %v", err)
	}
	if count != 2 {
		t.Fatalf("countModels(valid) = %d, want 2", count)
	}

	// Upstream dumps may omit CPA catalog-required fields; counting must still work.
	count, err = countModels([]byte(`{"models":[{"slug":"gpt-5.6-sol"}]}`))
	if err != nil {
		t.Fatalf("countModels(incomplete upstream model): %v", err)
	}
	if count != 1 {
		t.Fatalf("countModels(incomplete upstream model) = %d, want 1", count)
	}

	count, err = countModels([]byte(`{"models":[]}`))
	if err != nil {
		t.Fatalf("countModels(empty): %v", err)
	}
	if count != 0 {
		t.Fatalf("countModels(empty) = %d, want 0", count)
	}

	if _, err := countModels([]byte(`{"models":`)); err == nil {
		t.Fatal("countModels(malformed) error = nil, want error")
	}
	if _, err := countModels([]byte(`{}`)); err == nil {
		t.Fatal("countModels(missing models) error = nil, want error")
	}
}
