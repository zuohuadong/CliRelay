package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManagerExecute_ModelRouteBelowThresholdTargetsConfiguredModel(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			ModelRoutes: []internalconfig.ModelRouteRule{
				{
					Name: "codex-spark-under-128k",
					Match: internalconfig.ModelRouteMatch{
						RequestedModels: []string{"gpt-5.3-codex"},
					},
					Measure: internalconfig.ModelRouteMeasure{
						Source:    "estimated-input-tokens",
						OnMissing: "passthrough",
					},
					Routes: []internalconfig.ModelRouteBranch{
						{
							MaxInputTokens: 131072,
							Target: internalconfig.ModelRouteTarget{
								Provider:               "codex",
								Model:                  "gpt-5.3-codex-spark",
								PreserveRequestedModel: true,
							},
						},
						{
							MinInputTokens: 131073,
							Action:         "passthrough",
						},
					},
				},
			},
		},
	})
	executor := &openAICompatPoolExecutor{id: "codex"}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "codex-auth", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{
		{ID: "gpt-5.3-codex"},
		{ID: "gpt-5.3-codex-spark"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.3-codex"}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: int64(120000)},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != "gpt-5.3-codex-spark" {
		t.Fatalf("payload model = %q", string(resp.Payload))
	}
	if got := executor.ExecuteModels(); len(got) != 1 || got[0] != "gpt-5.3-codex-spark" {
		t.Fatalf("execute models = %#v", got)
	}
}

func TestManagerExecute_ModelRouteAboveThresholdPassthrough(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			ModelRoutes: []internalconfig.ModelRouteRule{
				{
					Match: internalconfig.ModelRouteMatch{
						RequestedModels: []string{"gpt-5.3-codex"},
					},
					Measure: internalconfig.ModelRouteMeasure{Source: "estimated-input-tokens", OnMissing: "passthrough"},
					Routes: []internalconfig.ModelRouteBranch{
						{
							MaxInputTokens: 131072,
							Target: internalconfig.ModelRouteTarget{
								Provider: "codex",
								Model:    "gpt-5.3-codex-spark",
							},
						},
						{
							MinInputTokens: 131073,
							Action:         "passthrough",
						},
					},
				},
			},
		},
	})
	executor := &openAICompatPoolExecutor{id: "codex"}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "codex-auth-passthrough", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{
		{ID: "gpt-5.3-codex"},
		{ID: "gpt-5.3-codex-spark"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.3-codex"}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.EstimatedInputTokensMetadataKey: int64(140000)},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != "gpt-5.3-codex" {
		t.Fatalf("payload model = %q", string(resp.Payload))
	}
	if got := executor.ExecuteModels(); len(got) != 1 || got[0] != "gpt-5.3-codex" {
		t.Fatalf("execute models = %#v", got)
	}
}
