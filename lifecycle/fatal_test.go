package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/lifecycle"
)

func TestFatalTriggersTheSameOrderedDrainAsCancellation(t *testing.T) {
	t.Parallel()
	var r recorder
	boom := errors.New("listener died")

	lc := lifecycle.New(lifecycle.Config{})
	lc.OnDrain(func() { r.add("drain") })
	aS, aStop := r.comp("a")
	a := lc.Add("a", aS, aStop)
	bS, bStop := r.comp("b")
	lc.Add("b", bS, bStop, lifecycle.After(a))

	done := make(chan error, 1)
	go func() { done <- lc.Run(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	lc.Fatal(boom)

	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Errorf("Run = %v, want boom", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Fatal did not wake Run within 5s")
	}

	want := []string{"start:a", "start:b", "drain", "stop:b", "stop:a"}
	if got := r.snapshot(); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
}

func TestFatalKeepsTheFirstErrorAndIsSafeToCallRepeatedly(t *testing.T) {
	t.Parallel()
	first := errors.New("first")
	second := errors.New("second")
	noop := func(context.Context) error { return nil }

	lc := lifecycle.New(lifecycle.Config{})
	lc.Add("a", noop, noop)

	done := make(chan error, 1)
	go func() { done <- lc.Run(context.Background()) }()
	time.Sleep(20 * time.Millisecond)

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); lc.Fatal(second) }()
	}
	lc.Fatal(first)
	wg.Wait()

	select {
	case err := <-done:
		if !errors.Is(err, first) && !errors.Is(err, second) {
			t.Errorf("Run = %v, want one of the reported errors", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}

	// Safe after Run has returned, and safe with nil.
	lc.Fatal(errors.New("late"))
	lc.Fatal(nil)
}

func TestFatalNilIsIgnored(t *testing.T) {
	t.Parallel()
	noop := func(context.Context) error { return nil }
	lc := lifecycle.New(lifecycle.Config{})
	lc.Add("a", noop, noop)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- lc.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	lc.Fatal(nil) // must not wake Run
	select {
	case <-done:
		t.Fatal("Fatal(nil) woke Run")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestFatalRacesCleanlyAgainstContextCancellation is the regression for a
// data race a reviewer caught: Run's select read fatalErr as a plain field,
// which is only safe on the <-l.fatalCh arm (close(fatalCh) supplies the
// happens-before there). On the <-ctx.Done() arm -- which wins when a signal
// and a component's Fatal call land at the same instant, an entirely
// realistic interleaving -- there was no synchronization at all between the
// write in Fatal and the read in Run.
//
// This test races cancel() and Fatal() from two goroutines with no
// synchronization between them, deliberately not giving either one a head
// start. It does not assert which of the two reasons Run reports -- that is
// genuinely arbitrary when they tie -- only that Run returns and that -race
// reports nothing.
func TestFatalRacesCleanlyAgainstContextCancellation(t *testing.T) {
	t.Parallel()
	noop := func(context.Context) error { return nil }
	lc := lifecycle.New(lifecycle.Config{})
	lc.Add("a", noop, noop)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- lc.Run(ctx) }()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); cancel() }()
	go func() { defer wg.Done(); lc.Fatal(errors.New("boom")) }()
	wg.Wait()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}
}

// TestFatalDuringDrainIsStillJoinedIntoRunsReturn pins an ordering that is
// invisible at the call site: Run loads fatalErr AFTER stopStarted, not
// before, specifically so a Fatal call landing while a component is still
// stopping is not lost. Moving that Load() above stopStarted looks like a
// harmless tidy-up -- coverage and mutation testing both wave it through,
// since neither exercises this particular interleaving -- but it would
// silently drop the fatal error for exactly this scenario.
//
// The scenario: cancel() wins the select (not Fatal), so the shutdown cause
// is "context cancelled" at the point Run enters drain/stop; a's Stop then
// blocks, and Fatal arrives while it is still running, well after drain
// started and before stopStarted returns.
func TestFatalDuringDrainIsStillJoinedIntoRunsReturn(t *testing.T) {
	t.Parallel()
	boom := errors.New("fatal during drain")
	stopStarted := make(chan struct{})
	release := make(chan struct{})

	lc := lifecycle.New(lifecycle.Config{})
	lc.Add("a",
		func(context.Context) error { return nil },
		func(context.Context) error {
			close(stopStarted)
			select {
			case <-release:
			case <-time.After(3 * time.Second):
			}
			return nil
		})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- lc.Run(ctx) }()

	time.Sleep(20 * time.Millisecond) // let Run reach its select
	cancel()                          // the ctx.Done() arm wins, not fatalCh

	select {
	case <-stopStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("a's Stop did not start within 5s")
	}

	lc.Fatal(boom) // arrives mid-drain, while a's Stop is still blocked
	close(release)

	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Errorf("Run = %v, want it to join a Fatal call that arrived during drain", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}
}
