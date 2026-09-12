package lifecycle

import (
	"testing"
	"time"
)

// TestDefaultStopTimeoutIs15Seconds pins the literal directly.
//
// Proving 14s or 16s wrong through the public API would mean a test that
// waits for one of those durations to elapse -- acceptable for a single run,
// but this suite is also run with -count=10 under -race, where a
// multi-second sleep in every iteration is exactly the kind of cost that
// pushes a suite towards being skipped rather than run. Reading the constant
// directly is a whitebox test by nature (New only exposes its effect once
// something actually waits 15s), which is why it lives in this package.
func TestDefaultStopTimeoutIs15Seconds(t *testing.T) {
	t.Parallel()
	if defaultStopTimeout != 15*time.Second {
		t.Errorf("defaultStopTimeout = %v, want 15s", defaultStopTimeout)
	}
}
