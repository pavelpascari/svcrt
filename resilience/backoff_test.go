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
