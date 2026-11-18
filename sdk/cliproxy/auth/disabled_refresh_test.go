package auth

import (
	"context"
	"testing"
	"time"
)

type refreshAlwaysRuntime struct{}

func (refreshAlwaysRuntime) ShouldRefresh(time.Time, *Auth) bool {
	return true
}

func TestManagerShouldRefreshSkipsDisabledAuths(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	now := time.Now()

	tests := []struct {
		name string
		auth *Auth
	}{
		{
			name: "disabled flag",
			auth: &Auth{
				ID:       "disabled-flag",
				Provider: "test-provider",
				Status:   StatusActive,
				Disabled: true,
				Runtime:  refreshAlwaysRuntime{},
			},
		},
		{
			name: "disabled status",
			auth: &Auth{
				ID:       "disabled-status",
				Provider: "test-provider",
				Status:   StatusDisabled,
				Runtime:  refreshAlwaysRuntime{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if mgr.shouldRefresh(tt.auth, now) {
				t.Fatal("shouldRefresh returned true for disabled auth")
			}
		})
	}
}

func TestCheckRefreshesSkipsDisabledAuth(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "disabled-auth",
		Provider: "test-provider",
		Status:   StatusDisabled,
		Disabled: true,
		Runtime:  refreshAlwaysRuntime{},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	refreshCalled := make(chan struct{}, 1)
	mgr.RegisterExecutor(&stubExecutor{
		onRefresh: func() {
			refreshCalled <- struct{}{}
		},
	})

	mgr.checkRefreshes(context.Background())

	select {
	case <-refreshCalled:
		t.Fatal("Refresh called for disabled auth")
	case <-time.After(100 * time.Millisecond):
	}
	current, ok := mgr.GetByID(auth.ID)
	if !ok {
		t.Fatal("expected disabled auth to remain registered")
	}
	if !current.Disabled || current.Status != StatusDisabled {
		t.Fatalf("disabled state changed to disabled=%v status=%q", current.Disabled, current.Status)
	}
}
