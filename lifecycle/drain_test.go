package lifecycle_test

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/lifecycle"
)

func TestOnDrainHooksRunBeforeAnyStop(t *testing.T) {
	t.Parallel()
	var r recorder

	lc := lifecycle.New(lifecycle.Config{})
	lc.OnDrain(func() { r.add("drain") })
	s, stop := r.comp("a")
	lc.Add("a", s, stop)

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"start:a", "drain", "stop:a"}
	if got := r.snapshot(); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
}

func TestOnDrainHooksRunInRegistrationOrder(t *testing.T) {
	t.Parallel()
	var r recorder

	lc := lifecycle.New(lifecycle.Config{})
	lc.OnDrain(func() { r.add("first") })
	lc.OnDrain(func() { r.add("second") })
	s, stop := r.comp("a")
	lc.Add("a", s, stop)

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatal(err)
	}
	got := r.snapshot()
	i, j := slices.Index(got, "first"), slices.Index(got, "second")
	if i < 0 || j < 0 || i > j {
		t.Errorf("ops = %v, want first before second", got)
	}
}

// The whole point of DrainDelay: readiness flips, THEN we wait, THEN we stop.
func TestDrainDelayElapsesBetweenHooksAndStop(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		hookAt time.Time
		stopAt time.Time
	)
	const delay = 120 * time.Millisecond

	lc := lifecycle.New(lifecycle.Config{DrainDelay: delay})
	lc.OnDrain(func() { mu.Lock(); hookAt = time.Now(); mu.Unlock() })
	lc.Add("a",
		func(context.Context) error { return nil },
		func(context.Context) error { mu.Lock(); stopAt = time.Now(); mu.Unlock(); return nil })

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gap := stopAt.Sub(hookAt); gap < delay {
		t.Errorf("stop began %v after the drain hook, want at least %v", gap, delay)
	}
}

// Stop must not inherit the cancelled run context, or every Shutdown returns
// instantly having drained nothing.
func TestStopContextIsNotCancelledButKeepsValues(t *testing.T) {
	t.Parallel()
	type key struct{}

	var (
		mu          sync.Mutex
		sawErr      error
		sawValue    any
		hadDeadline bool
	)

	lc := lifecycle.New(lifecycle.Config{})
	lc.Add("a",
		func(context.Context) error { return nil },
		func(ctx context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			sawErr = ctx.Err()
			sawValue = ctx.Value(key{})
			_, hadDeadline = ctx.Deadline()
			return nil
		})

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "kept"))
	done := make(chan error, 1)
	go func() { done <- lc.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}

	mu.Lock()
	defer mu.Unlock()
	if sawErr != nil {
		t.Errorf("Stop received a cancelled context (%v); it would drain nothing", sawErr)
	}
	if sawValue != "kept" {
		t.Errorf("Stop context lost the caller's values: got %v, want \"kept\"", sawValue)
	}
	if !hadDeadline {
		t.Error("Stop context had no deadline; StopTimeout was not applied")
	}
}

func TestStopTimeoutBoundsASlowStopAndOthersStillStop(t *testing.T) {
	t.Parallel()
	var r recorder

	lc := lifecycle.New(lifecycle.Config{StopTimeout: 50 * time.Millisecond})
	a := lc.Add("a",
		func(context.Context) error { return nil },
		func(context.Context) error { r.add("stop:a"); return nil })
	lc.Add("slow",
		func(context.Context) error { return nil },
		func(ctx context.Context) error {
			<-ctx.Done() // blocks until StopTimeout fires
			r.add("stop:slow")
			return ctx.Err()
		},
		lifecycle.After(a))

	err := runWithTimeout(t, lc, nil)
	if err == nil {
		t.Fatal("Run = nil, want the slow component's deadline error")
	}
	got := r.snapshot()
	if !slices.Contains(got, "stop:a") {
		t.Errorf("ops = %v, want a to stop even though slow timed out", got)
	}
}

// TestDrainDelayLogsOnlyWhenPositive pins the DrainDelay > 0 boundary exactly.
// At DrainDelay == 0 the "draining" log line (and the Sleep it guards) must
// not fire -- Config's doc says zero means no delay, and logging "draining,
// delay=0" would misreport that nothing is actually happening. At the
// smallest positive duration it must fire. A boundary weakened to >= 0 would
// wrongly log at zero; one narrowed to > 1ns would wrongly skip logging at
// exactly 1ns.
func TestDrainDelayLogsOnlyWhenPositive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		delay   time.Duration
		wantLog bool
	}{
		{"zero", 0, false},
		{"oneNanosecond", time.Nanosecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			lc := lifecycle.New(lifecycle.Config{Logger: log, DrainDelay: tc.delay})
			lc.OnDrain(func() {})
			lc.Add("a",
				func(context.Context) error { return nil },
				func(context.Context) error { return nil })

			if err := runWithTimeout(t, lc, nil); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(buf.String(), "draining"); got != tc.wantLog {
				t.Errorf("delay=%v: draining log present = %v, want %v (log: %s)", tc.delay, got, tc.wantLog, buf.String())
			}
		})
	}
}

func TestPerComponentStopTimeoutOverridesTheDefault(t *testing.T) {
	t.Parallel()
	var (
		mu sync.Mutex
		d  time.Duration
	)

	lc := lifecycle.New(lifecycle.Config{StopTimeout: 5 * time.Second})
	lc.Add("a",
		func(context.Context) error { return nil },
		func(ctx context.Context) error {
			dl, _ := ctx.Deadline()
			mu.Lock()
			d = time.Until(dl)
			mu.Unlock()
			return nil
		},
		lifecycle.StopTimeout(100*time.Millisecond))

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if d > time.Second {
		t.Errorf("stop deadline was %v away, want the 100ms override, not the 5s default", d)
	}
}

func TestRunWarnsWhenComponentsExistButNoDrainHookDoes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	lc := lifecycle.New(lifecycle.Config{Logger: log})
	lc.Add("a",
		func(context.Context) error { return nil },
		func(context.Context) error { return nil })

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no OnDrain") {
		t.Errorf("expected a warning about the missing drain hook, got: %s", buf.String())
	}
}

func TestRunDoesNotWarnWhenADrainHookExists(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	lc := lifecycle.New(lifecycle.Config{Logger: log})
	lc.OnDrain(func() {})
	lc.Add("a",
		func(context.Context) error { return nil },
		func(context.Context) error { return nil })

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "no OnDrain") {
		t.Errorf("warned despite a registered hook: %s", buf.String())
	}
}

func TestRunDoesNotWarnWithNoComponents(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	lc := lifecycle.New(lifecycle.Config{Logger: log})
	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "no OnDrain") {
		t.Errorf("warned for a lifecycle with no components: %s", buf.String())
	}
}
