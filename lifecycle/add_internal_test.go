package lifecycle

import (
	"context"
	"strings"
	"testing"
)

// TestAddRejectsOutOfRangeRefIndex covers the second half of Add's Ref
// validation. A Ref with this Lifecycle as its owner but an index past the
// end of its component list cannot arise through the public API — Add always
// returns a Ref whose index matches a component it just appended — so this
// whitebox test constructs one directly. Without this check, such a Ref (in
// particular one pointing at the very component being added, i.e. a forward
// reference) would resolve silently to whatever ends up at that index, which
// is exactly the silent-forward-reference failure levels() cannot itself
// guard against.
func TestAddRejectsOutOfRangeRefIndex(t *testing.T) {
	t.Parallel()
	noop := func(context.Context) error { return nil }

	lc := New(Config{})
	lc.Add("a", noop, noop)

	bad := Ref{owner: lc, i: 5}
	lc.Add("b", noop, noop, After(bad))

	err := lc.Run(context.Background())
	if err == nil {
		t.Fatal("Run accepted a Ref with an out-of-range index")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("err = %v, want it to name the out-of-range problem", err)
	}
}

// TestAddRejectsSelfReferencingRefIndex pins the exact boundary: a Ref whose
// index equals len(l.comps) at validation time -- i.e. one that (mis)points at
// the very component currently being added. r.i >= len(l.comps) must reject
// it; r.i > len(l.comps) (an off-by-one weakening) would let it through and
// resolve to whatever ends up at that index once the component is appended,
// which is exactly the silent forward-reference failure this check exists to
// rule out.
func TestAddRejectsSelfReferencingRefIndex(t *testing.T) {
	t.Parallel()
	noop := func(context.Context) error { return nil }

	lc := New(Config{})
	lc.Add("a", noop, noop) // len(l.comps) == 1 afterward

	bad := Ref{owner: lc, i: 1} // == len(l.comps) at the next Add call
	lc.Add("b", noop, noop, After(bad))

	err := lc.Run(context.Background())
	if err == nil {
		t.Fatal("Run accepted a self-referencing Ref")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("err = %v, want it to name the out-of-range problem", err)
	}
}

// TestAddRejectsNegativeRefIndex covers the r.i < 0 arm of the guard, which a
// Ref obtained through the public API can never trigger (Add only ever hands
// back non-negative indices). A same-owner, negative-index Ref can only be
// constructed from inside the package, which is exactly what this whitebox
// test does.
func TestAddRejectsNegativeRefIndex(t *testing.T) {
	t.Parallel()
	noop := func(context.Context) error { return nil }

	lc := New(Config{})
	lc.Add("a", noop, noop) // len(l.comps) == 1, so -1 is not caught by the upper bound

	bad := Ref{owner: lc, i: -1}
	lc.Add("b", noop, noop, After(bad))

	err := lc.Run(context.Background())
	if err == nil {
		t.Fatal("Run accepted a Ref with a negative index")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("err = %v, want it to name the out-of-range problem", err)
	}
}

// TestAddReportsEveryInvalidRefNotJustTheFirst pins continue over break in the
// afterRefs validation loop. Once a component already has one invalid Ref, its
// c.after slice is moot -- Run's err-count check short-circuits before
// levels() ever reads it -- so the only thing an invalid Ref past the first
// one could possibly change is whether it, too, gets its own reported error.
// continue keeps checking every remaining Ref; break would abandon the loop
// after the first bad one and swallow the rest, which is a real observable
// difference (the joined error would name only one bad Ref instead of both),
// not a cosmetic one.
func TestAddReportsEveryInvalidRefNotJustTheFirst(t *testing.T) {
	t.Parallel()
	noop := func(context.Context) error { return nil }

	lc := New(Config{})
	bad1 := Ref{owner: lc, i: -1}
	bad2 := Ref{owner: lc, i: -2}
	lc.Add("a", noop, noop, After(bad1, bad2))

	err := lc.Run(context.Background())
	if err == nil {
		t.Fatal("Run accepted invalid Refs")
	}
	if got := strings.Count(err.Error(), "out of range"); got != 2 {
		t.Errorf(`err = %q, want "out of range" reported twice (once per invalid Ref, not just the first)`, err)
	}
}

// TestNilFuncErrorJoinsWithRefError pins that a nil-StartFunc/StopFunc
// construction error and a bad-Ref construction error accumulate in the same
// l.errs slice and both survive errors.Join in Run, rather than one kind of
// construction error clobbering or short-circuiting the other.
func TestNilFuncErrorJoinsWithRefError(t *testing.T) {
	t.Parallel()
	noop := func(context.Context) error { return nil }

	lc := New(Config{})
	lc.Add("a", noop, noop)
	lc.Add("bad-ref", noop, noop, After(Ref{owner: lc, i: 5}))
	lc.Add("bad-nil", nil, noop)

	err := lc.Run(context.Background())
	if err == nil {
		t.Fatal("Run accepted both a bad Ref and a nil StartFunc")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("err = %v, want the bad-Ref error", err)
	}
	if !strings.Contains(err.Error(), "bad-nil") {
		t.Errorf("err = %v, want the nil-StartFunc error naming bad-nil", err)
	}
}
