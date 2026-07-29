package cliproxy

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestServiceApplyCoreAuthAddOrUpdate_DeleteReAddDoesNotInheritStaleRuntimeState(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	authID := "service-stale-state-auth"
	modelID := "stale-model"
	lastRefreshedAt := time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC)
	nextRefreshAfter := lastRefreshedAt.Add(30 * time.Minute)

	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authID)
	})

	service.applyCoreAuthAddOrUpdate(context.Background(), &coreauth.Auth{
		ID:               authID,
		Provider:         "claude",
		Status:           coreauth.StatusActive,
		LastRefreshedAt:  lastRefreshedAt,
		NextRefreshAfter: nextRefreshAfter,
		ModelStates: map[string]*coreauth.ModelState{
			modelID: {
				Quota: coreauth.QuotaState{BackoffLevel: 7},
			},
		},
	})

	service.applyCoreAuthRemoval(context.Background(), authID)

	if _, ok := service.coreManager.GetByID(authID); ok {
		t.Fatalf("expected auth %q to be removed from runtime state", authID)
	}

	service.applyCoreAuthAddOrUpdate(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "claude",
		Status:   coreauth.StatusActive,
	})

	updated, ok := service.coreManager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("expected re-added auth to be present")
	}
	if updated.Disabled {
		t.Fatalf("expected re-added auth to be active")
	}
	if !updated.LastRefreshedAt.IsZero() {
		t.Fatalf("expected LastRefreshedAt to reset on delete -> re-add, got %v", updated.LastRefreshedAt)
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("expected NextRefreshAfter to reset on delete -> re-add, got %v", updated.NextRefreshAfter)
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected ModelStates to reset on delete -> re-add, got %d entries", len(updated.ModelStates))
	}
	if models := registry.GetGlobalRegistry().GetModelsForClient(authID); len(models) == 0 {
		t.Fatalf("expected re-added auth to re-register models in global registry")
	}
}

func TestServiceHandleWatcherConfigAuthUpdateRegistersAstronModelsForScheduler(t *testing.T) {
	cfg := &config.Config{
		AstronCodeAPIKey: []config.OpenAICompatibility{{
			ResponseEndpoint: true,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "sk-test"},
			},
			Models: []config.OpenAICompatibilityModel{
				{Name: "xopdeepseekv4pro", Alias: "deepseek-v4-pro", ContextLength: 1000000},
			},
		}},
	}
	cfg.SanitizeAstronCode()

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.SetConfig(cfg)
	service := &Service{cfg: cfg, coreManager: manager}

	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("synthesize config auths: %v", err)
	}
	var astronAuth *coreauth.Auth
	for _, auth := range auths {
		if auth != nil && auth.Provider == "astron-code" {
			astronAuth = auth
			break
		}
	}
	if astronAuth == nil {
		t.Fatal("expected synthesized astron-code auth")
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(astronAuth.ID)
	})

	service.handleAuthUpdate(coreauth.WithSkipPersist(context.Background()), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     astronAuth.ID,
		Auth:   astronAuth,
	})

	if models := registry.GetGlobalRegistry().GetModelsForClient(astronAuth.ID); !hasServiceModelID(models, "deepseek-v4-pro") {
		t.Fatalf("expected deepseek-v4-pro to be registered for %s, got %#v", astronAuth.ID, models)
	}
	if registered, ok := manager.GetByID(astronAuth.ID); !ok || registered.Provider != "astron-code" {
		t.Fatalf("expected astron-code auth %s to be registered, got %#v", astronAuth.ID, registered)
	}
}

func TestServiceHandleWatcherConfigAuthUpdateRegistersDedicatedProviderModelsFromSynthesizedAuth(t *testing.T) {
	testCases := []struct {
		name          string
		provider      string
		firstAlias    string
		targetAPIKey  string
		configFactory func() *config.Config
	}{
		{
			name:         "astron code",
			provider:     "astron-code",
			firstAlias:   "gpt-5.3-codex",
			targetAPIKey: "astron-glm-key",
			configFactory: func() *config.Config {
				cfg := &config.Config{
					AstronCodeAPIKey: []config.OpenAICompatibility{
						{
							APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "astron-first-key"}},
							Models:        []config.OpenAICompatibilityModel{{Name: "astron-code-latest", Alias: "gpt-5.3-codex"}},
						},
						{
							APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "astron-glm-key"}},
							Models:        []config.OpenAICompatibilityModel{{Name: "xopglm52", Alias: "glm-5.2"}},
						},
					},
				}
				cfg.SanitizeAstronCode()
				return cfg
			},
		},
		{
			name:         "bigmodel coding",
			provider:     "bigmodel-coding",
			firstAlias:   "gpt-5.3-codex",
			targetAPIKey: "bigmodel-glm-key",
			configFactory: func() *config.Config {
				cfg := &config.Config{
					BigModelCodingAPIKey: []config.OpenAICompatibility{
						{
							APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "bigmodel-first-key"}},
							Models:        []config.OpenAICompatibilityModel{{Name: "glm-4.7", Alias: "gpt-5.3-codex"}},
						},
						{
							APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "bigmodel-glm-key"}},
							Models:        []config.OpenAICompatibilityModel{{Name: "glm-5.2", Alias: "glm-5.2"}},
						},
					},
				}
				cfg.SanitizeBigModelCoding()
				return cfg
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.configFactory()
			manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
			manager.SetConfig(cfg)
			service := &Service{cfg: cfg, coreManager: manager}

			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
				Config:      cfg,
				Now:         time.Now(),
				IDGenerator: synthesizer.NewStableIDGenerator(),
			})
			if err != nil {
				t.Fatalf("synthesize config auths: %v", err)
			}

			var targetAuth *coreauth.Auth
			for _, auth := range auths {
				if auth != nil && auth.Provider == tt.provider && auth.Attributes["api_key"] == tt.targetAPIKey {
					targetAuth = auth
					break
				}
			}
			if targetAuth == nil {
				t.Fatalf("expected synthesized %s auth for GLM-5.2", tt.provider)
			}
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(targetAuth.ID)
			})

			service.handleAuthUpdate(coreauth.WithSkipPersist(context.Background()), watcher.AuthUpdate{
				Action: watcher.AuthUpdateActionAdd,
				ID:     targetAuth.ID,
				Auth:   targetAuth,
			})

			models := registry.GetGlobalRegistry().GetModelsForClient(targetAuth.ID)
			if !hasServiceModelID(models, "glm-5.2") {
				t.Fatalf("expected GLM-5.2 to be registered for %s, got %#v", targetAuth.ID, models)
			}
			if hasServiceModelID(models, tt.firstAlias) {
				t.Fatalf("expected first entry alias %q not to be registered for %s, got %#v", tt.firstAlias, targetAuth.ID, models)
			}
			if registered, ok := manager.GetByID(targetAuth.ID); !ok || registered.Provider != tt.provider {
				t.Fatalf("expected %s auth %s to be registered, got %#v", tt.provider, targetAuth.ID, registered)
			}
		})
	}
}

func hasServiceModelID(models []*registry.ModelInfo, want string) bool {
	for _, model := range models {
		if model != nil && model.ID == want {
			return true
		}
	}
	return false
}

func TestForceHomeRuntimeConfigEnablesUsageStatistics(t *testing.T) {
	cfg := &config.Config{
		UsageStatisticsEnabled: false,
		SaveCooldownStatus:     true,
	}

	forceHomeRuntimeConfig(cfg)

	if !cfg.UsageStatisticsEnabled {
		t.Fatal("expected home runtime config to force usage statistics enabled")
	}
	if cfg.SaveCooldownStatus {
		t.Fatal("expected home runtime config to force cooldown status persistence disabled")
	}
}

func TestLifetimeRegistryObservesBarrierFromAppliedHomeConfig(t *testing.T) {
	registry := executionregistry.New()
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := internalconfig.DefaultCredentialInFlightConfig()
	cfg.SnapshotInterval = "30ms"

	if errApply := applyHomeInFlightPublisherConfig(manager, cfg); errApply != nil {
		t.Fatal(errApply)
	}
	applyHomeObservationBarrier(registry, 14)

	if freeze := registry.FreezeInFlight(time.Now().UTC()); freeze.BarrierRevision != 14 {
		t.Fatalf("barrier revision = %d, want 14", freeze.BarrierRevision)
	}
	if got := manager.HomeInFlightPublisherConfig(); got.SnapshotInterval != 30*time.Millisecond {
		t.Fatalf("publisher interval = %v, want 30ms", got.SnapshotInterval)
	}
}

func TestApplyHomeOverlayDoesNotApplyWithoutReadyClient(t *testing.T) {
	baseCfg := &config.Config{UsageStatisticsEnabled: false, SaveCooldownStatus: true}
	baseCfg.Home.Enabled = true
	service := &Service{cfg: baseCfg}

	service.applyHomeOverlay(&config.Config{
		UsageStatisticsEnabled: false,
		SaveCooldownStatus:     true,
	})

	if service.cfg == nil || service.cfg.UsageStatisticsEnabled {
		t.Fatal("unready home overlay changed usage statistics")
	}
	if !service.cfg.Home.Enabled {
		t.Fatal("unready home overlay changed local home settings")
	}
	if !service.cfg.SaveCooldownStatus {
		t.Fatal("unready home overlay changed cooldown status persistence")
	}
}
