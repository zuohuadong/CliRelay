package sharedstate

import (
	"context"
	"sync"
	"testing"
	"time"
)

// throttleSteps is one attack shape: bursts, a quiet period, and a peek, so the
// comparison covers the ladder, the reset and the refusal while blocked.
type throttleStep struct {
	advance time.Duration
	op      string // charge, peek, clear
}

func throttleScenario() []throttleStep {
	var steps []throttleStep
	steps = append(steps, throttleStep{op: "peek"})
	// Three failures reach the short limit and arm the first rung.
	for i := 0; i < 3; i++ {
		steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "charge"})
	}
	// Failures during the block are refused, not counted.
	steps = append(steps, throttleStep{advance: 200 * time.Millisecond, op: "charge"})
	steps = append(steps, throttleStep{op: "peek"})
	// The block expires; the next failures arm the second rung.
	steps = append(steps, throttleStep{advance: 2 * time.Second, op: "charge"})
	steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "charge"})
	steps = append(steps, throttleStep{advance: 100 * time.Millisecond, op: "charge"})
	steps = append(steps, throttleStep{advance: 4 * time.Second, op: "charge"})
	// A quiet period longer than reset-after wipes the counters.
	steps = append(steps, throttleStep{advance: time.Minute, op: "charge"})
	steps = append(steps, throttleStep{op: "peek"})
	// A successful login clears everything.
	steps = append(steps, throttleStep{advance: 10 * time.Millisecond, op: "clear"})
	steps = append(steps, throttleStep{advance: 10 * time.Millisecond, op: "peek"})
	steps = append(steps, throttleStep{advance: 10 * time.Millisecond, op: "charge"})
	return steps
}

func throttleTestSpec() ThrottleSpec {
	return ThrottleSpec{
		Short:      ThrottleWindow{Limit: 3, Window: 10 * time.Second, Slots: 5},
		Long:       ThrottleWindow{Limit: 6, Window: time.Minute, Slots: 4},
		Backoff:    []time.Duration{time.Second, 3 * time.Second, 10 * time.Second},
		ResetAfter: 30 * time.Second,
		Retention:  5 * time.Minute,
	}
}

// TestThrottlePGPortMatchesRedis runs the same attack against the Redis script
// and the PostgreSQL port and requires the same standing at every step. The
// port exists to behave identically, so agreement is the assertion.
func TestThrottlePGPortMatchesRedis(t *testing.T) {
	env := newTestEnv(t)
	store := env.node(t, "parity")
	env.clock.Set(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	key := unique(t, "throttle")
	spec := throttleTestSpec()

	state := throttleBucketState{}
	redisKey := throttleKey(key)
	_ = redisKey

	now := env.clock.Now()
	armed, escalated := 0, 0
	for i, step := range throttleScenario() {
		now = now.Add(step.advance)
		var want ThrottleState
		var err error
		switch step.op {
		case "charge":
			want, err = store.ThrottleCharge(ctx, key, spec, now)
		case "peek":
			want, err = store.ThrottlePeek(ctx, key, spec, now)
		case "clear":
			err = store.ThrottleClear(ctx, key)
			state = throttleBucketState{}
			continue
		}
		if err != nil {
			t.Fatalf("step %d (%s): redis: %v", i, step.op, err)
		}
		next, got, _ := throttleAdvance(state, step.op == "charge", spec, now)
		if step.op == "charge" {
			state = next
		}
		if want.NewlyArmed {
			armed++
			if want.Generation > escalated {
				escalated = want.Generation
			}
		}
		if got != want {
			t.Fatalf("step %d (%s at +%v):\n postgres %+v\n redis    %+v", i, step.op, now.Format("15:04:05.000"), got, want)
		}
	}
	// The scenario must actually reach the interesting states, or the parity
	// above proves nothing.
	if armed < 2 || escalated < 2 {
		t.Fatalf("scenario armed %d blocks with max generation %d, want at least 2 and 2", armed, escalated)
	}
}

func TestPGStoreThrottleArmsBlockAndClears(t *testing.T) {
	store := pgStoreTestStore(t)
	ctx := context.Background()
	key := "login-" + time.Now().Format("150405.000000")
	spec := throttleTestSpec()
	now := time.Now()

	var standing ThrottleState
	for i := 0; i < 3; i++ {
		var err error
		standing, err = store.ThrottleCharge(ctx, key, spec, now)
		if err != nil {
			t.Fatalf("charge %d: %v", i, err)
		}
	}
	if !standing.Blocked || !standing.NewlyArmed || standing.Generation != 1 {
		t.Fatalf("third failure must arm the first block: %+v", standing)
	}
	if standing.RetryAfter != spec.Backoff[0] {
		t.Fatalf("first block = %v, want %v", standing.RetryAfter, spec.Backoff[0])
	}

	// A failure while blocked is refused without being counted.
	blocked, err := store.ThrottleCharge(ctx, key, spec, now.Add(100*time.Millisecond))
	if err != nil {
		t.Fatalf("charge while blocked: %v", err)
	}
	if !blocked.Blocked || blocked.NewlyArmed || blocked.ShortCount != standing.ShortCount {
		t.Fatalf("charge while blocked changed the bucket: %+v vs %+v", blocked, standing)
	}

	peeked, err := store.ThrottlePeek(ctx, key, spec, now.Add(200*time.Millisecond))
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if !peeked.Blocked || peeked.Generation != 1 {
		t.Fatalf("peek must see the armed block: %+v", peeked)
	}

	if err := store.ThrottleClear(ctx, key); err != nil {
		t.Fatalf("clear: %v", err)
	}
	after, err := store.ThrottlePeek(ctx, key, spec, now.Add(300*time.Millisecond))
	if err != nil {
		t.Fatalf("peek after clear: %v", err)
	}
	if after.Blocked || after.ShortCount != 0 || after.Generation != 0 {
		t.Fatalf("clear must forget the bucket: %+v", after)
	}
}

func TestPGStoreThrottleCountsConcurrentFailures(t *testing.T) {
	store := pgStoreTestStore(t)
	ctx := context.Background()
	key := "concurrent-" + time.Now().Format("150405.000000")
	spec := throttleTestSpec()
	spec.Short.Limit = 1000 // high enough that no block is armed mid-run
	spec.Long.Limit = 1000
	now := time.Now()

	const guesses = 12
	var wg sync.WaitGroup
	errs := make(chan error, guesses)
	for i := 0; i < guesses; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.ThrottleCharge(ctx, key, spec, now); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent charge: %v", err)
	}
	// A charge reports the counts even when it does not trip the window, which
	// is how a lost update would show up: a peek does not, by design.
	final, err := store.ThrottleCharge(ctx, key, spec, now)
	if err != nil {
		t.Fatalf("final charge: %v", err)
	}
	// No failure may be lost to a lost update: the bucket must have counted
	// every one of the guesses exactly once.
	if final.ShortCount != guesses+1 || final.LongCount != guesses+1 {
		t.Fatalf("counted short=%d long=%d, want %d each", final.ShortCount, final.LongCount, guesses+1)
	}
}
