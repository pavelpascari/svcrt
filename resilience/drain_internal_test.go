package resilience

import (
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

// endlessBody models the case maxDrain exists for: an upstream whose error
// page does not end. It stops at 8*maxDrain rather than genuinely never
// ending, so that removing the cap FAILS these tests instead of hanging
// them -- an unbounded io.Copy from a truly endless reader never returns,
// and what a contributor who deleted the cap would get is ten minutes of
// apparently-wedged CI (go test's default timeout) and then a goroutine
// dump to interpret. The bound is 8x the cap so any plausible
// off-by-a-power-of-two in maxDrain is still well inside it, and the byte
// count stays exact: io.LimitReader yields exactly its limit before EOF
// while the underlying reader neither ends nor short-reads.
type endlessBody struct {
	read   atomic.Int64
	closed atomic.Bool
}

func (e *endlessBody) Read(p []byte) (int, error) {
	if e.read.Load() >= 8*maxDrain {
		return 0, io.EOF
	}
	e.read.Add(int64(len(p)))
	return len(p), nil
}

func (e *endlessBody) Close() error {
	e.closed.Store(true)
	return nil
}

// TestDrainIsBoundedAndStillCloses pins maxDrain. Without the cap -- i.e.
// io.Copy(io.Discard, resp.Body) instead of io.Copy(io.Discard,
// io.LimitReader(resp.Body, maxDrain)) -- drain reads the whole of
// endlessBody's 8*maxDrain and this test fails, in under a second, saying
// so.
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
