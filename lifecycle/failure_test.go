package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/lifecycle"
)

// TestStartFailureCancelsSiblingsAndWaitsForThem uses channels rather than a
// mutex-guarded bool read after Run returns, and a deliberate pause between
// "saw cancellation" and "returned". Against a plausible "abandon on first
// error" regression (return as soon as the first error arrives instead of
// waiting for the level), a bare post-hoc bool read races the background
// goroutine and catches the regression only by luck: cancellation propagates
// almost instantly, so the flag is often already set by the time Run returns
// even though Run never waited for it. The pause makes that race wide enough
// (200ms, far past ordinary scheduling jitter) that if Run truly didn't wait
// for the slow sibling, "returned" is reliably still open when Run's result
// arrives; happens-before through close(returned) -> wg.Done() -> wg.Wait()
// guarantees the correct implementation never fails this check.
func TestStartFailureCancelsSiblingsAndWaitsForThem(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	sawCancel := make(chan struct{})
	returned := make(chan struct{})

	fail := func(context.Context) error { return boom }
	slow := func(ctx context.Context) error {
		defer close(returned)
		select {
		case <-ctx.Done():
			close(sawCancel)
			time.Sleep(200 * time.Millisecond)
			return ctx.Err()
		case <-time.After(3 * time.Second):
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

	select {
	case <-sawCancel:
	default:
		t.Error("the slow sibling's Start was not cancelled")
	}
	select {
	case <-returned:
	default:
		t.Error("Run returned before the slow sibling's Start returned")
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
// the sequence: a <- {b, c} <- d, with b and c both at level 1.
//
// A pairwise index check alone (d before b/c, a after both) is not
// load-bearing here: Add/After force declaration order to already be a valid
// topological order, so a naive sequential reverse-declaration-order stop
// (no levels, no concurrency at all) satisfies any such pairwise check too.
// What actually distinguishes "stop the whole level concurrently" from
// "stop components one at a time in reverse order" is a rendezvous: b and c's
// Stop functions each block until BOTH have entered Stop. Under genuinely
// concurrent per-level stopping both proceed; under any sequential
// implementation the first one blocks alone until the test's timeout, since
// nothing ever starts the second one's Stop to release it.
func TestStopOrderAcrossMultiComponentLevels(t *testing.T) {
	t.Parallel()
	var r recorder

	aS, aStop := r.comp("a")
	lc := lifecycle.New(lifecycle.Config{})
	a := lc.Add("a", aS, aStop)

	bothIn := make(chan struct{}, 2)
	release := make(chan struct{})
	rendezvousStop := func(name string) lifecycle.StopFunc {
		return func(context.Context) error {
			bothIn <- struct{}{}
			if len(bothIn) == 2 {
				close(release)
			}
			select {
			case <-release:
				r.add("stop:" + name)
				return nil
			case <-time.After(3 * time.Second):
				t.Errorf("%s: Stop did not rendezvous with its sibling within 3s", name)
				return nil
			}
		}
	}

	bS, _ := r.comp("b")
	b := lc.Add("b", bS, rendezvousStop("b"), lifecycle.After(a))

	cS, _ := r.comp("c")
	c := lc.Add("c", cS, rendezvousStop("c"), lifecycle.After(a))

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

	// d must fully stop before either b or c begins stopping. This catches a
	// different bug class than the rendezvous does: advancing to the next
	// level before its WaitGroup has actually resolved.
	if dIdx > bIdx || dIdx > cIdx {
		t.Errorf("ops = %v, want stop:d before stop:b and stop:c", got)
	}

	// a must stop only after both b and c have finished.
	if aIdx < bIdx || aIdx < cIdx {
		t.Errorf("ops = %v, want stop:a after both stop:b and stop:c", got)
	}
}

// TestStartFailureJoinsUnwindStopError covers Run's startLevel-failure path:
// a's Start succeeds, so it must be unwound; its Stop fails during that
// unwind, while b (After a) fails to Start in the first place. Both errors
// must be reachable from what Run returns -- dropping the unwind's Stop
// error here would be exactly the kind of silent loss this task's "join
// every error" requirement is meant to rule out.
func TestStartFailureJoinsUnwindStopError(t *testing.T) {
	t.Parallel()
	startErr := errors.New("start boom")
	stopErr := errors.New("stop boom")

	lc := lifecycle.New(lifecycle.Config{})
	a := lc.Add("a",
		func(context.Context) error { return nil },
		func(context.Context) error { return stopErr },
	)
	lc.Add("b",
		func(context.Context) error { return startErr },
		func(context.Context) error { return nil },
		lifecycle.After(a),
	)

	err := lc.Run(context.Background())
	if !errors.Is(err, startErr) {
		t.Errorf("err = %v, want it to wrap the start error", err)
	}
	if !errors.Is(err, stopErr) {
		t.Errorf("err = %v, want it to also wrap the unwind's stop error", err)
	}
}
