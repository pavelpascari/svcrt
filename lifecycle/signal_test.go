package lifecycle_test

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/lifecycle"
)

func TestSignalContextCancelsOnSIGTERM(t *testing.T) {
	// Not parallel: it signals the whole process.
	ctx, stop := lifecycle.SignalContext(context.Background())
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not cancel the context within 5s")
	}
}

func TestSignalContextStopIsIdempotent(t *testing.T) {
	_, stop := lifecycle.SignalContext(context.Background())
	stop()
	stop() // must not panic
}

func TestSignalContextInheritsParentCancellation(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(context.Background())
	ctx, stop := lifecycle.SignalContext(parent)
	defer stop()

	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the parent did not cancel the signal context")
	}
}
