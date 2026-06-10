package auth

import (
	"context"
	"testing"
	"time"
)

type quotaProbeTestExecutor struct {
	schedulerProviderTestExecutor
	result *QuotaProbeResult
	err    error
}

func (e quotaProbeTestExecutor) Identifier() string { return "codex" }

func (e quotaProbeTestExecutor) ProbeQuotaRecovery(ctx context.Context, auth *Auth) (*QuotaProbeResult, error) {
	return e.result, e.err
}

func TestShouldProbeQuotaRequiresActiveCooldownAndProber(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, nil, nil)

	auth := quotaProbeTestAuth(now)
	if m.shouldProbeQuota(auth, now) {
		t.Fatal("expected no probe without executor")
	}

	m.RegisterExecutor(quotaProbeTestExecutor{})
	if !m.shouldProbeQuota(auth, now) {
		t.Fatal("expected probe for quota-blocked auth with prober")
	}

	m.quotaProbeAfter[auth.ID] = now.Add(time.Minute)
	if m.shouldProbeQuota(auth, now) {
		t.Fatal("expected scheduled probe delay to be respected")
	}

	auth.Disabled = true
	if m.shouldProbeQuota(auth, now.Add(2*time.Minute)) {
		t.Fatal("expected disabled auth to be skipped")
	}
}

func TestNextQuotaProbeTimeUsesRecoverWindowFraction(t *testing.T) {
	now := time.Now()
	auth := quotaProbeTestAuth(now)

	next := nextQuotaProbeTime(auth, now)
	min := now.Add(14 * time.Minute)
	max := now.Add(16 * time.Minute)
	if next.Before(min) || next.After(max) {
		t.Fatalf("nextQuotaProbeTime = %s, want about 15m after now", next.Sub(now))
	}

	auth.ModelStates["gpt-5"].NextRetryAfter = now.Add(2 * time.Minute)
	auth.ModelStates["gpt-5"].Quota.NextRecoverAt = now.Add(2 * time.Minute)
	next = nextQuotaProbeTime(auth, now)
	if next.Before(now.Add(quotaProbeMinInterval)) || next.After(now.Add(quotaProbeMinInterval+time.Second)) {
		t.Fatalf("short cooldown should clamp to min interval, got %s", next.Sub(now))
	}
}

func TestApplyQuotaProbeResultRecoveredModel(t *testing.T) {
	now := time.Now()
	auth := quotaProbeTestAuth(now)
	auth.Status = StatusError
	auth.StatusMessage = "quota"

	changed, models := applyQuotaProbeResult(auth, &QuotaProbeResult{Recovered: true}, now)
	if !changed {
		t.Fatal("expected recovered probe to change auth state")
	}
	if len(models) != 1 || models[0] != "gpt-5" {
		t.Fatalf("recovered models = %#v, want gpt-5", models)
	}
	state := auth.ModelStates["gpt-5"]
	if state.Unavailable || state.Quota.Exceeded || !state.NextRetryAfter.IsZero() {
		t.Fatalf("model state was not cleared: %#v", state)
	}
	if auth.Status != StatusActive || auth.StatusMessage != "" {
		t.Fatalf("auth status not restored: status=%s message=%q", auth.Status, auth.StatusMessage)
	}
}

func TestApplyQuotaProbeResultUpdatesRecoverAt(t *testing.T) {
	now := time.Now()
	auth := quotaProbeTestAuth(now)
	nextRecover := now.Add(10 * time.Minute)

	changed, models := applyQuotaProbeResult(auth, &QuotaProbeResult{
		Recovered:     false,
		NextRecoverAt: nextRecover,
	}, now)
	if !changed {
		t.Fatal("expected next recover update to change state")
	}
	if len(models) != 0 {
		t.Fatalf("recovered models = %#v, want none", models)
	}
	state := auth.ModelStates["gpt-5"]
	if !state.Unavailable || !state.Quota.Exceeded {
		t.Fatal("quota-blocked model should stay unavailable")
	}
	if !state.NextRetryAfter.Equal(nextRecover) || !state.Quota.NextRecoverAt.Equal(nextRecover) {
		t.Fatalf("recover timestamp not updated: next=%s quota=%s", state.NextRetryAfter, state.Quota.NextRecoverAt)
	}
}

func TestProbeQuotaRecoverySchedulesAfterUnrecoveredProbe(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(quotaProbeTestExecutor{result: &QuotaProbeResult{
		Recovered:     false,
		NextRecoverAt: now.Add(30 * time.Minute),
	}})
	auth := quotaProbeTestAuth(now)
	m.auths[auth.ID] = auth

	changed, err := m.probeQuotaRecovery(context.Background(), auth.ID, true)
	if err != nil {
		t.Fatalf("probeQuotaRecovery returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected probe to update recover timestamp")
	}
	if next := m.quotaProbeAfter[auth.ID]; next.IsZero() || next.Before(now) {
		t.Fatalf("expected next probe schedule, got %s", next)
	}
}

func quotaProbeTestAuth(now time.Time) *Auth {
	return &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "tok",
			"account_id":   "acct",
		},
		ModelStates: map[string]*ModelState{
			"gpt-5": {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "quota",
				NextRetryAfter: now.Add(time.Hour),
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: now.Add(time.Hour),
				},
			},
		},
	}
}
