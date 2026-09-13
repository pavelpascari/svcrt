package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/pavelpascari/svcrt/lifecycle"
)

func TestOnStartedHooksRunAfterEveryComponentHasStarted(t *testing.T) {
	t.Parallel()
	var r recorder

	lc := lifecycle.New(lifecycle.Config{})
	lc.OnStarted(func() { r.add("started") })
	sa, stopA := r.comp("a")
	a := lc.Add("a", sa, stopA)
	sb, stopB := r.comp("b")
	lc.Add("b", sb, stopB, lifecycle.After(a))

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The hook must land after BOTH starts, not after the first level. That
	// is the whole point: a readiness gate opened at the end of the first
	// level would answer 200 while a later component is still binding.
	want := []string{"start:a", "start:b", "started", "stop:b", "stop:a"}
	if got := r.snapshot(); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
}

func TestOnStartedHooksRunInRegistrationOrder(t *testing.T) {
	t.Parallel()
	var r recorder

	lc := lifecycle.New(lifecycle.Config{})
	lc.OnStarted(func() { r.add("first") })
	lc.OnStarted(func() { r.add("second") })
	s, stop := r.comp("a")
	lc.Add("a", s, stop)

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:a", "first", "second", "stop:a"}
	if got := r.snapshot(); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
}

// A failed startup must leave the hooks unrun. They are what opens the
// readiness and startup gates, so firing them for a stack that never came up
// would report a service ready that has already begun unwinding.
func TestOnStartedHooksDoNotRunWhenAStartFails(t *testing.T) {
	t.Parallel()
	var r recorder
	boom := errors.New("boom")

	lc := lifecycle.New(lifecycle.Config{})
	lc.OnStarted(func() { r.add("started") })
	sa, stopA := r.comp("a")
	a := lc.Add("a", sa, stopA)
	lc.Add("b",
		func(context.Context) error { r.add("start:b"); return boom },
		func(context.Context) error { r.add("stop:b"); return nil },
		lifecycle.After(a))

	err := runWithTimeout(t, lc, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("Run err = %v, want %v", err, boom)
	}
	if got := r.snapshot(); slices.Contains(got, "started") {
		t.Errorf("OnStarted hook ran despite a failed startup: ops = %v", got)
	}
}

// The hooks run before Run begins waiting, so a stack with no components at
// all still reports itself started rather than hanging closed forever.
func TestOnStartedHooksRunWithNoComponents(t *testing.T) {
	t.Parallel()
	var r recorder

	lc := lifecycle.New(lifecycle.Config{})
	lc.OnStarted(func() { r.add("started") })

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatal(err)
	}
	if got := r.snapshot(); !slices.Equal(got, []string{"started"}) {
		t.Errorf("ops = %v, want [started]", got)
	}
}
