package sharedstate

import (
	"context"
	"sync"
	"testing"
	"time"
)

func autoBanTestSpec() AutoBanSpec {
	return AutoBanSpec{
		Window:    time.Minute,
		Slots:     6,
		Threshold: 5,
		ClaimTTL:  10 * time.Second,
		Retention: 24 * time.Hour,
	}
}

func autoBanScenario() []throttleStep {
	steps := []throttleStep{}
	// Four failures stay below the threshold of five.
	for i := 0; i < 4; i++ {
		steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "charge"})
	}
	// The fifth crosses it and is claimed; the sixth is refused without being
	// counted, because the claim is in force.
	steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "charge"})
	steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "charge"})
	// The node writes the rule; the next failures see the ban.
	steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "mark"})
	steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "charge"})
	// The ban lapses (it was five minutes); the window was cleared by the mark,
	// so counting restarts from zero and a second crossing takes a second ban.
	steps = append(steps, throttleStep{advance: 6 * time.Minute, op: "charge"})
	for i := 0; i < 6; i++ {
		steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "charge"})
	}
	steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "mark"})
	steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "charge"})
	return steps
}

// TestAutoBanPGStoreMatchesPolicy replays one source's failures through the
// PostgreSQL store and the in-memory state machine it ports, and requires the
// same decision at every step.
func TestAutoBanPGStoreMatchesPolicy(t *testing.T) {
	store := pgStoreTestStore(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	cidr := "203.0.113.0/24-" + time.Now().Format("150405.000000")
	spec := autoBanTestSpec()

	state := autoBanState{}
	now := start
	claimed := 0
	for i, step := range autoBanScenario() {
		now = now.Add(step.advance)
		switch step.op {
		case "charge":
			want, err := store.AutoBanCharge(ctx, cidr, spec, now)
			if err != nil {
				t.Fatalf("step %d: store charge: %v", i, err)
			}
			next, got, _ := autoBanChargeAdvance(state.clone(), spec, now)
			state = next
			if got != want {
				t.Fatalf("step %d (charge):\n store   %+v\n policy  %+v", i, got, want)
			}
			if want.Claimed {
				claimed++
			}
		case "mark":
			until := now.Add(5 * time.Minute)
			if err := store.AutoBanMark(ctx, cidr, until, now, spec.Retention); err != nil {
				t.Fatalf("step %d: store mark: %v", i, err)
			}
			next, _ := autoBanMarkAdvance(state.clone(), until, now, spec.Retention)
			state = next
		}
	}
	if claimed != 2 {
		t.Fatalf("scenario claimed %d crossings, want 2", claimed)
	}
}

func TestPGStoreAutoBanClaimsExactlyOnce(t *testing.T) {
	store := pgStoreTestStore(t)
	ctx := context.Background()
	cidr := "198.51.100.7/32-" + time.Now().Format("150405.000000")
	spec := autoBanTestSpec()
	now := time.Now()

	// Eight nodes see the same failures at once; the threshold is five, so
	// exactly one of them may take the claim and write the rule.
	const nodes = 8
	charges := make([]AutoBanCharge, nodes)
	var wg sync.WaitGroup
	errs := make(chan error, nodes)
	for i := 0; i < nodes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := store.AutoBanCharge(ctx, cidr, spec, now.Add(time.Duration(i)*time.Millisecond))
			if err != nil {
				errs <- err
				return
			}
			charges[i] = result
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent charge: %v", err)
	}
	claims := 0
	for _, charge := range charges {
		if charge.Claimed {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("%d nodes claimed the crossing, want 1: %+v", claims, charges)
	}

	// The claiming node writes the rule; the counters restart and the ban is
	// visible to every node.
	until := now.Add(5 * time.Minute)
	if err := store.AutoBanMark(ctx, cidr, until, now, spec.Retention); err != nil {
		t.Fatalf("mark: %v", err)
	}
	after, err := store.AutoBanCharge(ctx, cidr, spec, now.Add(time.Second))
	if err != nil {
		t.Fatalf("charge after mark: %v", err)
	}
	if !after.AlreadyBanned || after.Claimed || after.Bans != 1 {
		t.Fatalf("after mark = %+v, want already banned with one ban", after)
	}

	// A second crossing after the ban lapses is counted again, with the
	// escalation counter preserved.
	later := until.Add(time.Second)
	for i := 0; i < spec.Threshold-1; i++ {
		if _, err := store.AutoBanCharge(ctx, cidr, spec, later.Add(time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatalf("charge while lapsed: %v", err)
		}
	}
	crossed, err := store.AutoBanCharge(ctx, cidr, spec, later.Add(time.Second))
	if err != nil {
		t.Fatalf("second crossing: %v", err)
	}
	if !crossed.Claimed || crossed.Bans != 1 {
		t.Fatalf("second crossing = %+v, want claimed with one earlier ban", crossed)
	}
}
