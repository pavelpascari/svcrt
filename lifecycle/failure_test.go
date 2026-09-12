package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/lifecycle"
)

func TestStartFailureCancelsSiblingsAndWaitsForThem(t *testing.T) {
	t.Parallel()

	var (
		mu            sync.Mutex
		slowReturned  bool
		slowSawCancel bool
	)
	boom := errors.New("boom")

	fail := func(context.Context) error { return boom }
	slow := func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			mu.Lock()
			slowSawCancel, slowReturned = true, true
			mu.Unlock()
			return ctx.Err()
		case <-time.After(3 * time.Second):
			mu.Lock()
			slowReturned = true
			mu.Unlock()
			return nil
		}
	}
	noop := func(context.Context) error { return nil }

	lc := lifecycle.New(lifecycle.Config{})
	lc.Add("fails", fail, noop)
	lc.Add("slow", slow, noop)

	done := make(chan error, 1)
	go func() { done <- lc.Run(context.Background()) }()

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}

	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap boom", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slowReturned {
		t.Error("Run returned before the slow sibling's Start returned")
	}
	if !slowSawCancel {
		t.Error("the slow sibling's Start was not cancelled")
	}
}

func TestStartFailureStopsOnlyStartedComponentsInReverse(t *testing.T) {
	t.Parallel()
	var r recorder
	boom := errors.New("boom")

	aS, aStop := r.comp("a")
	a := func(ctx context.Context) error { return aS(ctx) }

	lc := lifecycle.New(lifecycle.Config{})
	ref := lc.Add("a", a, aStop)
	lc.Add("b", func(context.Context) error { r.add("start:b"); return boom },
		func(context.Context) error { r.add("stop:b"); return nil },
		lifecycle.After(ref))

	if err := lc.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}

	got := r.snapshot()
	want := []string{"start:a", "start:b", "stop:a"}
	if !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v (b failed to start, so it must not be stopped)", got, want)
	}
}

func TestStartFailureJoinsEveryError(t *testing.T) {
	t.Parallel()
	first := errors.New("first")
	second := errors.New("second")
	noop := func(context.Context) error { return nil }

	lc := lifecycle.New(lifecycle.Config{})
	lc.Add("one", func(context.Context) error { return first }, noop)
	lc.Add("two", func(context.Context) error { return second }, noop)

	err := lc.Run(context.Background())
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Errorf("err = %v, want both first and second joined", err)
	}
	if !strings.Contains(err.Error(), "one") || !strings.Contains(err.Error(), "two") {
		t.Errorf("err = %q, want both component names", err)
	}
}

func TestStartFailureAtOneLevelDoesNotStartTheNext(t *testing.T) {
	t.Parallel()
	var r recorder
	boom := errors.New("boom")
	noop := func(context.Context) error { return nil }

	lc := lifecycle.New(lifecycle.Config{})
	bad := lc.Add("bad", func(context.Context) error { r.add("start:bad"); return boom }, noop)
	lc.Add("next", func(context.Context) error { r.add("start:next"); return nil }, noop,
		lifecycle.After(bad))

	if err := lc.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if slices.Contains(r.snapshot(), "start:next") {
		t.Error("a component started even though its dependency failed")
	}
}

// TestStopOrderAcrossMultiComponentLevels pins the level boundary, not merely
// the sequence: a <- {b, c} <- d, with b and c both at level 1. A chain test
// (one component per level) cannot distinguish "d stops before its whole
// dependency level finishes" from "d stops before the next name in a list" --
// here that only shows up if we make one of b/c slow to stop and confirm a
// waits for both, not just the first one back.
func TestStopOrderAcrossMultiComponentLevels(t *testing.T) {
	t.Parallel()
	var r recorder

	aS, aStop := r.comp("a")
	lc := lifecycle.New(lifecycle.Config{})
	a := lc.Add("a", aS, aStop)

	bS, _ := r.comp("b")
	bSlowStop := func(context.Context) error {
		time.Sleep(50 * time.Millisecond)
		r.add("stop:b")
		return nil
	}
	b := lc.Add("b", bS, bSlowStop, lifecycle.After(a))

	cS, cStop := r.comp("c")
	c := lc.Add("c", cS, cStop, lifecycle.After(a))

	dS, dStop := r.comp("d")
	lc.Add("d", dS, dStop, lifecycle.After(b, c))

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := r.snapshot()

	dIdx := slices.Index(got, "stop:d")
	bIdx := slices.Index(got, "stop:b")
	cIdx := slices.Index(got, "stop:c")
	aIdx := slices.Index(got, "stop:a")

	if dIdx < 0 || bIdx < 0 || cIdx < 0 || aIdx < 0 {
		t.Fatalf("ops = %v, missing an expected stop", got)
	}

	// d must fully stop before either b or c begins stopping.
	if dIdx > bIdx || dIdx > cIdx {
		t.Errorf("ops = %v, want stop:d before stop:b and stop:c", got)
	}

	// a must stop only after both b and c have finished, i.e. after the
	// later of the two -- this is what pins the level boundary rather than
	// a mere sequence, since b is slow and c is not.
	if aIdx < bIdx || aIdx < cIdx {
		t.Errorf("ops = %v, want stop:a after both stop:b and stop:c", got)
	}
}
