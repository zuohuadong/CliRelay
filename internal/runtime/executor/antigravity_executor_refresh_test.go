package executor

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestAntigravityEnsureAccessTokenPreservesCallerCancellation(t *testing.T) {
	t.Helper()

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	ctx := context.WithValue(cancelledCtx, util.ContextKeyRoundTripper, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if err := req.Context().Err(); !errors.Is(err, context.Canceled) {
			t.Fatalf("refresh request context err = %v, want %v", err, context.Canceled)
		}
		return nil, req.Context().Err()
	}))

	executor := &AntigravityExecutor{
		cfg: &config.Config{
			OAuthClients: config.OAuthClients{
				Antigravity: config.OAuthClient{
					ClientID:     "client-id",
					ClientSecret: "client-secret",
				},
			},
		},
	}
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"refresh_token": "refresh-token",
		},
	}

	_, _, err := executor.ensureAccessToken(ctx, auth)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ensureAccessToken error = %v, want context.Canceled", err)
	}
}
