package modelcapacity

import (
	"testing"
	"time"
)

func TestCoolingRemembersOneWindow(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	now := time.Now()

	Mark("gpt-6.2-sol", now)
	if !Cooling("gpt-6.2-sol", now) {
		t.Fatal("a model just refused must be cooling")
	}
	if !Cooling("  GPT-6.2-Sol ", now) {
		t.Fatal("the same model must match whatever its spacing and case")
	}
	if Cooling("gpt-6.1-sol", now) {
		t.Fatal("a model never refused must not be cooling")
	}
	if Cooling("gpt-6.2-sol", now.Add(Window)) {
		t.Fatal("the memory must not outlive its window")
	}
}

func TestMarkIgnoresAnEmptyModel(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	Mark("  ", time.Now())
	if Cooling("", time.Now()) {
		t.Fatal("an empty model name must not be remembered")
	}
}

func TestForgetEndsTheWindow(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	now := time.Now()
	Mark("alpha", now)
	Forget("alpha")
	if Cooling("alpha", now) {
		t.Fatal("a model that served must not stay cooling")
	}
}

// The thinking suffix names a level of one model, not another model.
func TestCoolingIgnoresTheThinkingSuffix(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	now := time.Now()
	Mark("gpt-6.2-sol(high)", now)
	if !Cooling("gpt-6.2-sol", now) {
		t.Fatal("the suffix must not make it a different model")
	}
}

func TestExpiredEntriesArePruned(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	now := time.Now()
	Mark("alpha", now.Add(-2*Window))
	Mark("beta", now)
	mu.Lock()
	_, keptAlpha := refused["alpha"]
	_, keptBeta := refused["beta"]
	mu.Unlock()
	if keptAlpha || !keptBeta {
		t.Fatalf("expired entries = %v/%v, want alpha pruned and beta kept", keptAlpha, keptBeta)
	}
}
