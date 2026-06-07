package registry

import "testing"

func TestReconcileRemovedOpenRouterModelsUnregistersGlobalClient(t *testing.T) {
	modelID := "openrouter-test/stale-model"
	registerOpenRouterModel(&openRouterModel{
		ID:            modelID,
		Name:          "Stale Model",
		ContextLength: 128000,
	})

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
