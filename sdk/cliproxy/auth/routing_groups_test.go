package auth

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestAllowedChannelGroupsFromMetadataParsesStringList(t *testing.T) {
	t.Parallel()

	allowed := allowedChannelGroupsFromMetadata(map[string]any{
		"allowed-channel-groups": " Pro,team-a,,PRO ",
	})

	if len(allowed) != 2 {
		t.Fatalf("allowed group count = %d, want 2", len(allowed))
	}
	if _, ok := allowed["pro"]; !ok {
		t.Fatal("expected normalized group pro")
	}
	if _, ok := allowed["team-a"]; !ok {
		t.Fatal("expected normalized group team-a")
	}
}

func TestCanServeModelWithScopesSupportsAllowedGroupPrefixedModels(t *testing.T) {
	t.Parallel()

	reg := registry.GetGlobalRegistry()
	now := time.Now().Unix()
	reg.RegisterClient("pro-auth", "openai", []*registry.ModelInfo{
		{ID: "pro/gpt-5", Created: now},
	})
	t.Cleanup(func() {
		reg.UnregisterClient("pro-auth")
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "pro-auth",
		Provider: "openai",
		Prefix:   "pro",
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	allowedGroups := map[string]struct{}{"pro": {}}
	if !manager.CanServeModelWithScopes("gpt-5", nil, allowedGroups, "") {
		t.Fatal("expected unprefixed model to be available through allowed pro group")
	}
}
