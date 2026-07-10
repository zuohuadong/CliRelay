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
