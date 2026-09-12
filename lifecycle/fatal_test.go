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
