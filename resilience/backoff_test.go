package resilience_test

import (
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/resilience"
)

func TestConstantReturnsTheSameDelayEveryAttempt(t *testing.T) {
	t.Parallel()
	b := resilience.Constant(250 * time.Millisecond)
	for attempt := 1; attempt <= 5; attempt++ {
		if got := b(attempt); got != 250*time.Millisecond {
			t.Errorf("attempt %d = %v, want 250ms", attempt, got)
		}
	}
}

func TestExponentialDoublesUntilItSaturates(t *testing.T) {
	t.Parallel()
	b := resilience.Exponential(100*time.Millisecond, 800*time.Millisecond)
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 800 * time.Millisecond},
		{5, 800 * time.Millisecond}, // saturated
		{9, 800 * time.Millisecond}, // still saturated, no overflow
		{64, 800 * time.Millisecond},
	} {
		if got := b(tc.attempt); got != tc.want {
			t.Errorf("attempt %d = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// A caller that passes 0 or a negative attempt should get the base delay
// rather than a nonsense value: the loop is 1-based and this is the guard.
func TestExponentialTreatsAttemptsBelowOneAsTheFirst(t *testing.T) {
	t.Parallel()
	b := resilience.Exponential(100*time.Millisecond, 800*time.Millisecond)
	for _, attempt := range []int{0, -1, -100} {
		if got := b(attempt); got != 100*time.Millisecond {
			t.Errorf("attempt %d = %v, want 100ms", attempt, got)
		}
	}
}

// Jitter's contract is a RANGE, not a value. Assert the range holds, and that
// the result is not constant -- a Jitter that returned its input unchanged
// would satisfy the range but defeat the purpose.
func TestJitterStaysInRangeAndVaries(t *testing.T) {
	t.Parallel()
	base := 500 * time.Millisecond
	b := resilience.Jitter(resilience.Constant(base))

	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		d := b(1)
		if d < 0 || d > base {
			t.Fatalf("jittered delay %v outside [0, %v]", d, base)
		}
		seen[d] = true
	}
	if len(seen) < 10 {
		t.Errorf("jitter produced only %d distinct values in 200 draws; it is not jittering", len(seen))
	}
}

func TestJitterOfAZeroDelayIsZero(t *testing.T) {
	t.Parallel()
	b := resilience.Jitter(resilience.Constant(0))
	if got := b(1); got != 0 {
		t.Errorf("jitter of a zero delay = %v, want 0", got)
	}
}

// Jitter's contract only promises a range for a well-behaved Backoff, but a
// negative one (a bad custom Backoff, or a bug in one) must still be handled
// safely rather than being handed to rand.N, which panics for a
// non-positive argument.
func TestJitterOfANegativeDelayIsZeroWithoutPanicking(t *testing.T) {
	t.Parallel()
	b := resilience.Jitter(resilience.Constant(-1 * time.Nanosecond))
	if got := b(1); got != 0 {
		t.Errorf("jitter of a negative delay = %v, want 0", got)
	}
}

// Full jitter promises the CLOSED interval [0, b(attempt)]: the top value is
// reachable, not just approached. At the smallest positive delay (1ns) the
// interval has exactly two members, {0, 1ns}, which makes both the lower
// and the upper bound directly observable over enough draws -- unlike the
// large-base range test above, which cannot tell a range missing its top
// nanosecond from one that has it.
func TestJitterAtTheSmallestPositiveDelayCoversTheFullInclusiveRange(t *testing.T) {
	t.Parallel()
	base := 1 * time.Nanosecond
	b := resilience.Jitter(resilience.Constant(base))

	sawZero, sawBase := false, false
	for i := 0; i < 200; i++ {
		d := b(1)
		if d < 0 || d > base {
			t.Fatalf("jittered delay %v outside [0, %v]", d, base)
		}
		switch d {
		case 0:
			sawZero = true
		case base:
			sawBase = true
		}
	}
	if !sawZero || !sawBase {
		t.Errorf("200 draws at base=1ns: sawZero=%v sawBase=%v, want both -- the range must be the closed interval [0, base]", sawZero, sawBase)
	}
}

// base > max is not forbidden anywhere, and a caller who transposes the two
// arguments should still get a sane delay rather than one that ignores max.
func TestExponentialSaturatesWhenBaseAlreadyExceedsMax(t *testing.T) {
	t.Parallel()
	b := resilience.Exponential(2*time.Second, 500*time.Millisecond)
	for _, attempt := range []int{1, 2, 5} {
		if got := b(attempt); got != 500*time.Millisecond {
			t.Errorf("attempt %d = %v, want 500ms (max), not the larger base", attempt, got)
		}
	}
}

// The saturation check is "d >= max/2", not "d >= max/3" or any other
// fraction. With a max that is not a clean power-of-two multiple of base,
// the choice of divisor changes which attempt first saturates: max/3 fires
// one doubling earlier than the delay has actually earned it, returning max
// when the honest doubled value is still comfortably under it.
func TestExponentialSaturationBoundaryIsHalfMaxNotAnyOtherFraction(t *testing.T) {
	t.Parallel()
	b := resilience.Exponential(100*time.Millisecond, 900*time.Millisecond)
	// Doubling: 100, 200, 400, 800. At attempt 4, d=400 is checked against
	// max/2=450 before the final doubling to 800 -- 400 < 450, so this must
	// NOT saturate yet. A max/3=300 boundary would wrongly trigger at d=400.
	if got := b(4); got != 800*time.Millisecond {
		t.Errorf("attempt 4 = %v, want 800ms (not yet saturated -- max/2 is 450ms, and 400ms hasn't reached it)", got)
	}
}

// Doubling a time.Duration close to the top of int64 must not be allowed to
// overflow while deciding whether to saturate. The guard computes max/2
// (safe, no overflow risk) rather than comparing against 2*d or max*2 (which
// can wrap negative for a max this large) -- this pins that choice: with a
// max near 2^62, checking against an overflowed max*2 would make the
// saturation check spuriously true on the very first attempt.
func TestExponentialGuardDoesNotOverflowForAMaxNearInt64Max(t *testing.T) {
	t.Parallel()
	max := time.Duration(1) << 62
	b := resilience.Exponential(1*time.Nanosecond, max)
	if got := b(2); got != 2*time.Nanosecond {
		t.Errorf("attempt 2 = %v, want 2ns -- an overflowing saturation check would wrongly return max (%v) here", got, max)
	}
}
