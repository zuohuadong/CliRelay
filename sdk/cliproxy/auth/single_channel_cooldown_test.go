package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestMarkResultSingleProviderDoesNotCooldownModel(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "single-provider-model", Provider: "codex"}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5",
		Error: &Error{
			Code:       "upstream_error",
			Message:    "upstream unavailable",
			HTTPStatus: http.StatusBadGateway,
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth missing after MarkResult")
	}
	state := updated.ModelStates["gpt-5"]
	if state == nil {
		t.Fatal("model state missing after MarkResult")
	}
	if state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("single provider model was cooled: unavailable=%t next=%v", state.Unavailable, state.NextRetryAfter)
	}
	if state.Quota.Exceeded || !state.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("single provider model quota state was cooled: %+v", state.Quota)
	}
}

func TestMarkResultSingleProviderDoesNotCooldownAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "single-provider-auth", Provider: "codex"}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Error: &Error{
			Code:       "rate_limit",
			Message:    "quota exhausted",
			HTTPStatus: http.StatusTooManyRequests,
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth missing after MarkResult")
	}
	if updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("single provider auth was cooled: unavailable=%t next=%v", updated.Unavailable, updated.NextRetryAfter)
	}
	if updated.Quota.Exceeded || !updated.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("single provider auth quota state was cooled: %+v", updated.Quota)
	}
}

func TestMarkResultMultipleProvidersStillCooldowns(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	for _, auth := range []*Auth{
		{ID: "multi-provider-codex", Provider: "codex"},
		{ID: "multi-provider-claude", Provider: "claude"},
	} {
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("Register %s returned error: %v", auth.ID, errRegister)
		}
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "multi-provider-codex",
		Provider: "codex",
		Model:    "gpt-5",
		Error: &Error{
			Code:       "upstream_error",
			Message:    "upstream unavailable",
			HTTPStatus: http.StatusBadGateway,
		},
	})

	updated, ok := manager.GetByID("multi-provider-codex")
	if !ok || updated == nil || updated.ModelStates["gpt-5"] == nil {
		t.Fatal("multi-provider model state missing after MarkResult")
	}
	state := updated.ModelStates["gpt-5"]
	if !state.Unavailable || state.NextRetryAfter.IsZero() || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("multiple providers did not retain cooldown: unavailable=%t next=%v", state.Unavailable, state.NextRetryAfter)
	}
}
