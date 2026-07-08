package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_ModelOverridesAndRoutes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`
model-overrides:
  - channel: " CoDeX "
    model: " gpt-5.3-codex-spark "
    priority: 100
    context-length: 131072
    max-completion-tokens: 32768
routing:
  strategy: round-robin
  model-routes:
    - name: " codex-spark-under-128k "
      match:
        requested-models:
          - " gpt-5.3-codex "
      measure:
        source: estimated-input-tokens
        on-missing: passthrough
      routes:
        - max-input-tokens: 131072
          target:
            provider: codex
            model: gpt-5.3-codex-spark
            preserve-requested-model: true
        - min-input-tokens: 131073
          action: passthrough
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if len(cfg.ModelOverrides) != 1 {
		t.Fatalf("expected 1 model override, got %d", len(cfg.ModelOverrides))
	}
	override := cfg.ModelOverrides[0]
	if override.Channel != "codex" || override.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("unexpected override identity: %#v", override)
	}
	if override.Priority != 100 || override.ContextLength != 131072 || override.MaxCompletionTokens != 32768 {
		t.Fatalf("unexpected override metadata: %#v", override)
	}

	if len(cfg.Routing.ModelRoutes) != 1 {
		t.Fatalf("expected 1 model route, got %d", len(cfg.Routing.ModelRoutes))
	}
	route := cfg.Routing.ModelRoutes[0]
	if route.Name != "codex-spark-under-128k" {
		t.Fatalf("route name = %q", route.Name)
	}
	if len(route.Match.RequestedModels) != 1 || route.Match.RequestedModels[0] != "gpt-5.3-codex" {
		t.Fatalf("unexpected route match: %#v", route.Match.RequestedModels)
	}
	if route.Measure.Source != "estimated-input-tokens" || route.Measure.OnMissing != "passthrough" {
		t.Fatalf("unexpected measure config: %#v", route.Measure)
	}
	if len(route.Routes) != 2 {
		t.Fatalf("expected 2 route branches, got %d", len(route.Routes))
	}
	if route.Routes[0].MaxInputTokens != 131072 || route.Routes[0].Target.Provider != "codex" || route.Routes[0].Target.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("unexpected below-threshold route: %#v", route.Routes[0])
	}
	if !route.Routes[0].Target.PreserveRequestedModel {
		t.Fatalf("expected preserve-requested-model on below-threshold route")
	}
	if route.Routes[1].MinInputTokens != 131073 || route.Routes[1].Action != "passthrough" {
		t.Fatalf("unexpected above-threshold route: %#v", route.Routes[1])
	}
}
