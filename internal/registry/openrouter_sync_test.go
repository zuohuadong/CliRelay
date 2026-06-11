package registry

import (
	"testing"
	"time"
)

func TestReconcileRemovedOpenRouterModelsUnregistersGlobalClient(t *testing.T) {
	modelID := "openrouter-test/stale-model"
	GetGlobalRegistry().RegisterClient(openRouterSyncClientID(modelID), "openrouter", []*ModelInfo{{ID: modelID}})

	if !GetGlobalRegistry().IsModelConfigured(modelID) {
		t.Fatalf("expected synced model %q to be configured before removal", modelID)
	}

	state := &OpenRouterSyncState{
		registeredModels: map[string]*openRouterModelEntry{
			modelID: {ID: modelID, Name: "Stale Model"},
		},
	}
	reconcileRemovedOpenRouterModels(state, map[string]struct{}{})

	if _, ok := state.registeredModels[modelID]; ok {
		t.Fatalf("expected synced model %q to be removed from sync state", modelID)
	}
	if GetGlobalRegistry().IsModelConfigured(modelID) {
		t.Fatalf("expected stale synced model %q to be unregistered globally", modelID)
	}
}

func TestSyncOpenRouterModelsDoesNotRegisterAvailableModels(t *testing.T) {
	modelID := "openrouter-test/catalog-only-model"
	state := &OpenRouterSyncState{}

	result := syncOpenRouterModels(state, []openRouterModel{{
		ID:            modelID,
		Name:          "Catalog Only Model",
		ContextLength: 128000,
	}}, time.Now())

	if result.Seen != 1 || result.Added != 1 {
		t.Fatalf("sync result = %+v, want seen=1 added=1", result)
	}
	if _, ok := state.registeredModels[modelID]; !ok {
		t.Fatalf("expected synced model %q to be recorded in sync state", modelID)
	}
	if GetGlobalRegistry().IsModelConfigured(modelID) {
		t.Fatalf("catalog-only synced model %q should not be globally available", modelID)
	}
}
