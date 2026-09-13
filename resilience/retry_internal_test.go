package resilience

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// canWait's contract is "strictly more time than d", not "at least d": if
// exactly d remains, waiting d would leave nothing for the attempt that
// follows. now is passed in explicitly (see canWait's doc comment) so the
// exact boundary -- remaining == d -- is constructible without racing the
// real clock.
func TestCanWaitBoundaryIsStrictlyGreaterThanD(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	const d = 5 * time.Second

	ctx, cancel := context.WithDeadline(context.Background(), now.Add(d))
	defer cancel()

	if canWait(ctx, d, now) {
		t.Error("canWait with exactly d remaining = true, want false -- no room is left for the attempt after the wait")
	}
	if !canWait(ctx, d-1, now) {
		t.Error("canWait with d-1 remaining (more than d) = false, want true")
	}
}

// wait's `d <= 0` guard must return ctx.Err() directly rather than falling
// through to a real timer + select: if it fell through, an already-done ctx
// would race an already-fired zero-duration timer, occasionally returning
// nil instead of the cancellation error. The guard makes that race
// impossible by never starting the timer at all. Repeated to make a
// probabilistic mismatch (observed at roughly 50% per call for a
// mistakenly-removed guard, in an isolated reproduction) overwhelmingly
// likely to surface if the guard is gone.
func TestWaitOfANonPositiveDelayNeverRacesAnAlreadyDoneContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for i := 0; i < 200; i++ {
		if err := wait(ctx, 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: wait(cancelled ctx, 0) = %v, want context.Canceled", i, err)
		}
	}
}

// A positive d, even the smallest possible one, must take the real
// timer/select path rather than the `d <= 0` fast return -- that is the
// difference between "no wait needed" and "an actual (if tiny) wait was
// asked for". The fast path is essentially free; the timer path measurably
// is not, so elapsed time distinguishes them without needing nanosecond
// timing precision anywhere in the assertion.
func TestWaitOfAPositiveDelayTakesTheTimerPathNotTheFastReturn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const n = 5000

	start := time.Now()
	for i := 0; i < n; i++ {
		if err := wait(ctx, 1); err != nil {
			t.Fatalf("iteration %d: wait(background, 1ns) = %v, want nil", i, err)
		}
	}
	elapsed := time.Since(start)

	// A real Duration.NewTimer + select consistently takes low-single-digit
	// microseconds per call in an isolated reproduction (~600ns/call, ~3ms
	// total for 5000 calls); the fast-return path takes low nanoseconds per
	// call (~12µs total for 5000 calls). 1ms sits comfortably between the two,
	// with roughly two orders of magnitude of margin on each side.
	if elapsed < time.Millisecond {
		t.Errorf("%d calls to wait(ctx, 1ns) took %v, want at least 1ms -- the d<=0 fast path must not be taken for a positive delay", n, elapsed)
	}
}

// drain must tolerate a response whose Body is nil -- reachable whenever a
// RoundTripper returns a non-nil *http.Response without setting Body, which
// http.RoundTripper's contract does not forbid.
func TestDrainToleratesANilBody(t *testing.T) {
	t.Parallel()
	drain(&http.Response{StatusCode: http.StatusServiceUnavailable, Body: nil})
}

// The default Backoff is Jitter(Exponential(100ms, 2s)). Jitter makes any
// single draw non-deterministic, but the base literal is still pinned
// precisely: at attempt 1 (unsaturated, so the pre-jitter value is exactly
// base) the draw is always in [0, 100ms], and over enough draws the top of
// that range -- anything past 99ms -- must be reached. A base of 99ms could
// never produce such a draw; a base of 101ms (or 0, from an integer-division
// typo like 100/time.Millisecond) would violate the range check instead.
func TestDefaultBackoffBaseIsHundredMilliseconds(t *testing.T) {
	t.Parallel()
	p := Policy{}.withDefaults()

	const base = 100 * time.Millisecond
	sawNearTop := false
	for i := 0; i < 5000; i++ {
		d := p.Backoff(1)
		if d < 0 || d > base {
			t.Fatalf("attempt 1 draw %v outside [0, %v]", d, base)
		}
		if d > 99*time.Millisecond {
			sawNearTop = true
		}
	}
	if !sawNearTop {
		t.Error("5000 draws at attempt 1 never exceeded 99ms; want some in (99ms, 100ms] -- the base may not be 100ms")
	}
}

// Mirrors TestDefaultBackoffBaseIsHundredMilliseconds for the 2s cap. Attempt
// 20 is well past saturation for a 100ms base doubling toward any of the
// candidate caps this pins against (1s, 2s, 3s, or 0), so the pre-jitter
// value is deterministically exactly the cap.
func TestDefaultBackoffCapIsTwoSeconds(t *testing.T) {
	t.Parallel()
	p := Policy{}.withDefaults()

	const cap_ = 2 * time.Second
	sawNearTop := false
	for i := 0; i < 5000; i++ {
		d := p.Backoff(20)
		if d < 0 || d > cap_ {
			t.Fatalf("attempt 20 draw %v outside [0, %v]", d, cap_)
		}
		if d > 1980*time.Millisecond {
			sawNearTop = true
		}
	}
	if !sawNearTop {
		t.Error("5000 draws at attempt 20 never exceeded 1.98s; want some in (1.98s, 2s] -- the cap may not be 2s")
	}
}
