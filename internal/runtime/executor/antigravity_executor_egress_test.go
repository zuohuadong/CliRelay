package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type antigravityEgressResolver struct {
	resolved egress.ResolvedEndpoint
	bound    bool
	err      error
	provider string
	authID   string
}

func (r *antigravityEgressResolver) ResolveProviderSelection(_ context.Context, provider, authID string) (egress.ResolvedEndpoint, bool, error) {
	r.provider = provider
	r.authID = authID
	return r.resolved, r.bound, r.err
}

func TestAntigravityResolveEgressAuthUsesOptionalSharedSelection(t *testing.T) {
	auth := antigravityWarmEgressAuth("antigravity-user.json", "http://credential-proxy.example:8080")
	unbound := &antigravityEgressResolver{}
	exec := NewAntigravityExecutorWithEgress(&config.Config{}, unbound)

	resolved, err := exec.resolveEgressAuth(context.Background(), auth)
	if err != nil || resolved != auth || resolved.ProxyURL != auth.ProxyURL {
		t.Fatalf("unbound resolve = %#v, %v", resolved, err)
	}

	selected := &antigravityEgressResolver{
		bound: true,
		resolved: egress.ResolvedEndpoint{
			Endpoint: egress.Endpoint{ID: "shared-hk"},
			ProxyURL: "socks5://10.77.0.2:1080",
		},
	}
	exec = NewAntigravityExecutorWithEgress(&config.Config{}, selected)
	resolved, err = exec.resolveEgressAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("bound resolve error = %v", err)
	}
	if resolved == auth || resolved.ProxyURL != selected.resolved.ProxyURL || auth.ProxyURL != "http://credential-proxy.example:8080" {
		t.Fatalf("resolved=%#v original=%#v", resolved, auth)
	}
	if selected.provider != "antigravity" || selected.authID != auth.ID {
		t.Fatalf("resolver call provider=%q authID=%q", selected.provider, selected.authID)
	}
}

func TestAntigravityEnsureAccessTokenReturnsRoutedCloneOnlyWhenBound(t *testing.T) {
	auth := antigravityWarmEgressAuth("antigravity-user.json", "")
	unboundExec := NewAntigravityExecutorWithEgress(&config.Config{}, &antigravityEgressResolver{})
	token, updated, err := unboundExec.ensureAccessToken(context.Background(), auth)
	if err != nil || token != "warm-token" || updated != nil {
		t.Fatalf("unbound ensureAccessToken token=%q updated=%#v err=%v", token, updated, err)
	}

	boundExec := NewAntigravityExecutorWithEgress(&config.Config{}, &antigravityEgressResolver{
		bound: true,
		resolved: egress.ResolvedEndpoint{
			Endpoint: egress.Endpoint{ID: "shared-sg"},
			ProxyURL: "http://127.0.0.1:18080",
		},
	})
	token, updated, err = boundExec.ensureAccessToken(context.Background(), auth)
	if err != nil || token != "warm-token" || updated == nil || updated.ProxyURL != "http://127.0.0.1:18080" {
		t.Fatalf("bound ensureAccessToken token=%q updated=%#v err=%v", token, updated, err)
	}
	if auth.ProxyURL != "" {
		t.Fatalf("original auth proxy mutated to %q", auth.ProxyURL)
	}
}

func TestAntigravityBoundEndpointFailureDoesNotFallBack(t *testing.T) {
	auth := antigravityWarmEgressAuth("antigravity-user.json", "http://fallback.example:8080")
	exec := NewAntigravityExecutorWithEgress(&config.Config{}, &antigravityEgressResolver{
		bound: true,
		err:   egress.ErrEndpointDisabled,
	})
	_, err := exec.resolveEgressAuth(context.Background(), auth)
	if !errors.Is(err, egress.ErrEndpointDisabled) {
		t.Fatalf("resolve error = %v, want ErrEndpointDisabled", err)
	}
}

func antigravityWarmEgressAuth(id, proxyURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "antigravity",
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"access_token": "warm-token",
			"expired":      time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
}
