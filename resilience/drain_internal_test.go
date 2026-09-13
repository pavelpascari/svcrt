package resilience

import (
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

// endlessBody yields bytes forever and records how many were read. It models
// the case maxDrain exists for: an upstream whose error page never ends.
type endlessBody struct {
	read   atomic.Int64
	closed atomic.Bool
}

func (e *endlessBody) Read(p []byte) (int, error) {
	e.read.Add(int64(len(p)))
	return len(p), nil
}

func (e *endlessBody) Close() error {
	e.closed.Store(true)
	return nil
}

// TestDrainIsBoundedAndStillCloses pins maxDrain. Without the cap -- i.e.
// io.Copy(io.Discard, resp.Body) instead of io.Copy(io.Discard,
// io.LimitReader(resp.Body, maxDrain)) -- this test does not fail, it HANGS:
// an unbounded copy from an endless reader never returns, so drain never
// gets to the Close call this test checks. Run it with -timeout when
// deliberately breaking the cap to confirm that; a panic with a goroutine
// dump naming io.Copy is the expected evidence of the missing cap, not a
// clean test failure.
func TestDrainIsBoundedAndStillCloses(t *testing.T) {
	t.Parallel()
	body := &endlessBody{}
	drain(&http.Response{StatusCode: http.StatusServiceUnavailable, Body: body})

	if got := body.read.Load(); got > maxDrain {
		t.Errorf("drain read %d bytes, want at most %d -- the cap is not applied", got, int64(maxDrain))
	}
	if body.read.Load() == 0 {
		t.Error("drain read nothing; it is not draining at all")
	}
	if !body.closed.Load() {
		t.Error("drain did not close the body")
	}
}

// TestDrainCapIsExactly64KiB pins maxDrain's literal value, in bytes,
// against a hardcoded constant rather than the maxDrain symbol itself.
// TestDrainIsBoundedAndStillCloses above checks "at most maxDrain", using
// the same symbol on both sides of the comparison -- so it cannot detect the
// literal changing (63<<10, 64<<9, 65<<10, 64<<11 all still satisfy "at most
// maxDrain" trivially). An endless body makes the byte count read by drain
// deterministic: io.LimitReader always yields exactly its limit before EOF
// when the underlying reader never itself returns EOF or a short read.
func TestDrainCapIsExactly64KiB(t *testing.T) {
	t.Parallel()
	body := &endlessBody{}
	drain(&http.Response{StatusCode: http.StatusServiceUnavailable, Body: body})

	const want = 64 << 10
	if got := body.read.Load(); got != want {
		t.Errorf("drain read %d bytes from an endless body, want exactly %d (64KiB)", got, int64(want))
	}
}

// A body under the cap must be drained to EOF and closed, so its connection
// is reused -- the ordinary case.
func TestDrainReadsAShortBodyToEOFAndCloses(t *testing.T) {
	t.Parallel()
	body := &countingCloser{Reader: io.LimitReader(neverEnding{}, 1024)}
	drain(&http.Response{StatusCode: http.StatusServiceUnavailable, Body: body})

	if body.n != 1024 {
		t.Errorf("drain read %d bytes of a 1024-byte body; it stopped early", body.n)
	}
	if !body.closed {
		t.Error("drain did not close the body")
	}
}

type neverEnding struct{}

func (neverEnding) Read(p []byte) (int, error) { return len(p), nil }

type countingCloser struct {
	io.Reader
	n      int
	closed bool
}

func (c *countingCloser) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.n += n
	return n, err
}

func (c *countingCloser) Close() error { c.closed = true; return nil }
