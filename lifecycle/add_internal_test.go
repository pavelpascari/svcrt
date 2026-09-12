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
