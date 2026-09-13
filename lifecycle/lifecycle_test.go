package lifecycle_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/lifecycle"
)

// recorder captures the order of start and stop calls across goroutines.
type recorder struct {
	mu  sync.Mutex
	ops []string
}

func (r *recorder) add(op string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, op)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ops)
}

// comp returns start/stop funcs that record their calls.
func (r *recorder) comp(name string) (lifecycle.StartFunc, lifecycle.StopFunc) {
	return func(context.Context) error { r.add("start:" + name); return nil },
		func(context.Context) error { r.add("stop:" + name); return nil }
}

// runWithTimeout runs lc until cancel, failing rather than hanging.
func runWithTimeout(t *testing.T, lc *lifecycle.Lifecycle, before func()) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- lc.Run(ctx) }()

	// Let Run reach its wait, then trigger shutdown.
	time.Sleep(20 * time.Millisecond)
	if before != nil {
		before()
	}
	cancel()

	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s after cancellation")
		return nil
	}
}

func TestRunStartsInDependencyOrderAndStopsInReverse(t *testing.T) {
	t.Parallel()
	var r recorder

	lc := lifecycle.New(lifecycle.Config{})
	aS, aStop := r.comp("a")
	a := lc.Add("a", aS, aStop)
	bS, bStop := r.comp("b")
	b := lc.Add("b", bS, bStop, lifecycle.After(a))
	cS, cStop := r.comp("c")
	lc.Add("c", cS, cStop, lifecycle.After(b))

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"start:a", "start:b", "start:c", "stop:c", "stop:b", "stop:a"}
	if got := r.snapshot(); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
}

func TestRunStartsIndependentComponentsConcurrently(t *testing.T) {
	t.Parallel()

	// Two components at the same level, each blocking until the other has
	// entered Start. If they were started sequentially this deadlocks, and the
	// timeout fails the test rather than hanging the suite.
	// once guards the close: nothing drains bothIn, so once both sends land
	// len(bothIn) stays 2 forever, and if both sends complete before either
	// length check runs -- ordinary parallelism, no preemption needed -- both
	// goroutines take the branch and the second close panics. That would
	// surface as an unreproducible "close of closed channel" under
	// -race -count=10, in the module whose whole point is that flakes get
	// caught here rather than in production.
	var once sync.Once
	bothIn := make(chan struct{}, 2)
	release := make(chan struct{})
	blocking := func(context.Context) error {
		bothIn <- struct{}{}
		if len(bothIn) == 2 {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
			return nil
		case <-time.After(3 * time.Second):
			return context.DeadlineExceeded
		}
	}
	noop := func(context.Context) error { return nil }

	lc := lifecycle.New(lifecycle.Config{})
	lc.Add("x", blocking, noop)
	lc.Add("y", blocking, noop)

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatalf("independent components did not start concurrently: %v", err)
	}
}

func TestRunWithNoComponentsReturnsNil(t *testing.T) {
	t.Parallel()
	lc := lifecycle.New(lifecycle.Config{})
	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}

func TestAddRefFromAnotherLifecycleIsAnError(t *testing.T) {
	t.Parallel()
	noop := func(context.Context) error { return nil }

	other := lifecycle.New(lifecycle.Config{})
	other.Add("o0", noop, noop)
	foreign := other.Add("o1", noop, noop) // index 1

	// The receiving Lifecycle already has components at indices 0 and 1, so a
	// bounds-only check would accept this Ref and silently point at "b".
	lc := lifecycle.New(lifecycle.Config{})
	lc.Add("a", noop, noop)
	lc.Add("b", noop, noop)
	lc.Add("c", noop, noop, lifecycle.After(foreign))

	err := lc.Run(context.Background())
	if err == nil {
		t.Fatal("Run accepted a Ref from another Lifecycle")
	}
	if !strings.Contains(err.Error(), "different Lifecycle") {
		t.Errorf("err = %v, want it to name the foreign-Ref problem", err)
	}
}
