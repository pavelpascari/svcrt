package lifecycle_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

// TestSignalContextDoesNotCancelBeforeAnySignal guards the wait itself: the
// goroutine that restores default handling must do so only after ctx.Done(),
// not immediately. signal.NotifyContext's stop function both unregisters the
// relay AND cancels the context, so a goroutine that called stop() up front
// (skipping the receive) would cancel ctx instantly, on every call, with no
// signal and no parent cancellation involved -- silently defeating the whole
// point of SignalContext while every other test here still passed.
func TestSignalContextDoesNotCancelBeforeAnySignal(t *testing.T) {
	t.Parallel()
	ctx, stop := lifecycle.SignalContext(context.Background())
	defer stop()

	select {
	case <-ctx.Done():
		t.Fatal("context was cancelled before any signal or parent cancellation")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestSignalContextSecondSignalRestoresDefaultHandling proves the doc
// comment's central claim: after the first signal, a second one terminates
// the process via the OS's default disposition, not lifecycle catching it
// again. That can only be observed by actually letting a process die, so this
// test re-execs the test binary as a child, sends it two SIGTERMs, and checks
// the child was killed by the second one rather than still running (or having
// exited some other way).
//
// Not parallel: it manages a real child process and its own signal timing.
func TestSignalContextSecondSignalRestoresDefaultHandling(t *testing.T) {
	const childEnv = "LIFECYCLE_SIGNAL_CONTEXT_CHILD"

	if os.Getenv(childEnv) == "1" {
		ctx, stop := lifecycle.SignalContext(context.Background())
		defer stop()
		<-ctx.Done() // first SIGTERM: caught and turned into cancellation
		// Default handling is now restored. Block so only the OS -- via a
		// second SIGTERM -- can end this process.
		select {}
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestSignalContextSecondSignalRestoresDefaultHandling$")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	// Give the child time to reach SignalContext and register its handler.
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("first SIGTERM: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("second SIGTERM: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("child exited with %v, want a signal-terminated *exec.ExitError", err)
		}
		ws, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
			t.Errorf("child exit status = %v, want terminated by SIGTERM (default handling restored)", exitErr)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child did not terminate on the second SIGTERM within 5s; default handling was not restored")
	}
}
