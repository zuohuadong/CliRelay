package clusterruntime

import (
	"context"
	"testing"
	"time"
)

func pgQuotaTestAdapters(t *testing.T) (*pgQuotaAdapter, *pgQuotaAdapter) {
	t.Helper()
	store, _ := pgAffinityTestStore(t)
	a := newPGQuotaAdapter(store, nil, "node-a")
	b := newPGQuotaAdapter(store, nil, "node-b")
	t.Cleanup(func() {
		a.Close()
		b.Close()
	})
	return a, b
}

func TestPGQuotaSharesWindowsBetweenNodes(t *testing.T) {
	a, b := pgQuotaTestAdapters(t)
	ctx := context.Background()
	subject := "key-" + time.Now().Format("150405.000000")

	first, err := a.AdmitRequest(ctx, subject, 0)
	if err != nil {
		t.Fatalf("admit on node A: %v", err)
	}
	if first.RPM != 1 {
		t.Fatalf("first request RPM = %v, want 1", first.RPM)
	}
	second, err := b.AdmitRequest(ctx, subject, 0)
	if err != nil {
		t.Fatalf("admit on node B: %v", err)
	}
	// Node B must see node A's request: the window is cluster-wide.
	if second.RPM < 1.9 || second.RPM > 2.1 {
		t.Fatalf("second request RPM = %v, want about 2", second.RPM)
	}

	// The dashboard reads the same window.
	rates, err := a.Rates(ctx, []string{subject})
	if err != nil {
		t.Fatalf("rates: %v", err)
	}
	if got := rates[subject].RPM; got < 1.9 || got > 2.1 {
		t.Fatalf("dashboard RPM = %v, want about 2", got)
	}
}

func TestPGQuotaSharesConcurrencySlots(t *testing.T) {
	a, b := pgQuotaTestAdapters(t)
	ctx := context.Background()
	subject := "conc-" + time.Now().Format("150405.000000")
	const limit = 2

	first, err := a.AdmitRequest(ctx, subject, limit)
	if err != nil {
		t.Fatalf("admit 1: %v", err)
	}
	second, err := b.AdmitRequest(ctx, subject, limit)
	if err != nil {
		t.Fatalf("admit 2: %v", err)
	}
	if !first.SlotTaken || !second.SlotTaken {
		t.Fatalf("the first two requests must both take a slot: %+v %+v", first, second)
	}
	third, err := a.AdmitRequest(ctx, subject, limit)
	if err != nil {
		t.Fatalf("admit 3: %v", err)
	}
	if third.SlotTaken {
		t.Fatalf("the third request must be refused a slot: %+v", third)
	}
	if third.Held != limit {
		t.Fatalf("held = %d, want %d", third.Held, limit)
	}
	// Releasing frees the slot for the next request, on either node.
	first.Release()
	first.Release() // idempotent
	deadline := time.Now().Add(5 * time.Second)
	for {
		next, err := b.AdmitRequest(ctx, subject, limit)
		if err != nil {
			t.Fatalf("admit after release: %v", err)
		}
		if next.SlotTaken {
			next.Release()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a released slot never became available")
		}
		time.Sleep(20 * time.Millisecond)
	}
	second.Release()
}

func TestPGQuotaFlushesCountedRequestsAndTokens(t *testing.T) {
	a, _ := pgQuotaTestAdapters(t)
	ctx := context.Background()
	subject := "counted-" + time.Now().Format("150405.000000")

	a.CountRequest(subject)
	a.CountRequest(subject)
	a.AddTokens(subject, 150)
	a.AddTokens(subject, 50)
	a.flush()

	rates, err := a.Rates(ctx, []string{subject})
	if err != nil {
		t.Fatalf("rates: %v", err)
	}
	if got := rates[subject].RPM; got < 1.9 || got > 2.1 {
		t.Fatalf("counted requests = %v, want 2", got)
	}
	if got := rates[subject].TPM; got < 199 || got > 201 {
		t.Fatalf("counted tokens = %v, want 200", got)
	}
}
