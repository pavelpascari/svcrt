package resilience_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/resilience"
)

// rtFunc is a local RoundTripper adapter. This module cannot import
// httpclient, so the test defines its own.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// respond builds a minimal response with a readable body.
func respond(code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader("body")),
		Header:     make(http.Header),
	}
}

func get(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://x.invalid/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}

func TestRetryStopsAtMaxAttempts(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 3,
		Backoff:     resilience.Constant(0),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestRetryReturnsTheFirstSuccess(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 5,
		Backoff:     resilience.Constant(0),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) < 3 {
			return respond(http.StatusBadGateway), nil
		}
		return respond(http.StatusOK), nil
	}))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (two failures then a success)", got)
	}
}

func TestRetryRetriesATransportError(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	boom := errors.New("dial refused")
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 2,
		Backoff:     resilience.Constant(0),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, boom
	}))

	if _, err := rt.RoundTrip(get(t)); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

// The status set is a policy decision, not an accident. 500 is deliberately
// absent: a genuine server bug repeats, and retrying amplifies load on an
// already-broken dependency.
func TestDefaultRetryIfCoversTheRightStatuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code      int
		wantRetry bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
		{http.StatusInternalServerError, false},
		{http.StatusNotImplemented, false},
		{http.StatusBadRequest, false},
		{http.StatusNotFound, false},
		{http.StatusOK, false},
	} {
		var calls atomic.Int64
		rt := resilience.Retry(resilience.Policy{
			MaxAttempts: 2,
			Backoff:     resilience.Constant(0),
		})(rtFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return respond(tc.code), nil
		}))
		resp, err := rt.RoundTrip(get(t))
		if err != nil {
			t.Fatalf("%d: RoundTrip: %v", tc.code, err)
		}
		resp.Body.Close()

		want := int64(1)
		if tc.wantRetry {
			want = 2
		}
		if got := calls.Load(); got != want {
			t.Errorf("status %d: attempts = %d, want %d", tc.code, got, want)
		}
	}
}

// Idempotency gate. A lost response does not mean the request did not happen,
// so a POST is sent exactly once by default.
func TestNonIdempotentMethodsAreNotRetriedByDefault(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		var calls atomic.Int64
		rt := resilience.Retry(resilience.Policy{
			MaxAttempts: 3,
			Backoff:     resilience.Constant(0),
		})(rtFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return respond(http.StatusServiceUnavailable), nil
		}))

		req, err := http.NewRequestWithContext(context.Background(), method, "http://x.invalid/", strings.NewReader("payload"))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("%s: RoundTrip: %v", method, err)
		}
		resp.Body.Close()
		if got := calls.Load(); got != 1 {
			t.Errorf("%s: attempts = %d, want 1 (not retried by default)", method, got)
		}
	}
}

func TestRetryMethodsOverridesTheDefaultSet(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts:  3,
		Backoff:      resilience.Constant(0),
		RetryMethods: []string{http.MethodPost},
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://x.invalid/", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (POST opted in)", got)
	}
}

// Spec 5.2: do not sleep into an attempt the deadline guarantees will be
// cancelled. Return the last result while it is still usable.
func TestBackoffDoesNotSleepPastTheDeadline(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 5,
		Backoff:     resilience.Constant(10 * time.Second),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://x.invalid/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	start := time.Now()
	resp, err := rt.RoundTrip(req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
	if elapsed > time.Second {
		t.Errorf("returned after %v; it slept into a deadline it could not meet", elapsed)
	}
}

func TestCancellationDuringBackoffAborts(t *testing.T) {
	t.Parallel()
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 5,
		Backoff:     resilience.Constant(5 * time.Second),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusServiceUnavailable), nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://x.invalid/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		resp, err := rt.RoundTrip(req)
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the context did not abort the backoff sleep")
	}
}

// Test that defaults are applied when Policy is empty.
func TestDefaultsAreApplied(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (default MaxAttempts)", got)
	}
}

// Test that backoff timer actually fires when context doesn't cancel.
func TestBackoffTimerFires(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 2,
		Backoff:     resilience.Constant(50 * time.Millisecond),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	start := time.Now()
	resp, err := rt.RoundTrip(get(t))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if got := calls.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("elapsed = %v, want at least 40ms (backoff didn't sleep)", elapsed)
	}
}

// Test that empty request method is treated as GET (idempotent).
func TestEmptyMethodIsTreatedAsGet(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 2,
		Backoff:     resilience.Constant(0),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	req, err := http.NewRequestWithContext(context.Background(), "", "http://x.invalid/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if got := calls.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2 (empty method is idempotent)", got)
	}
}

// Test that ineligible methods don't retry.
func TestIneligibleMethodDoesntRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 3,
		Backoff:     resilience.Constant(0),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://x.invalid/", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (POST is not eligible by default)", got)
	}
}

// net/http documents an empty Request.Method as meaning GET. A caller who
// builds a request literal rather than calling http.NewRequest gets one, and
// it must be retried like the GET it is -- not silently skipped because ""
// is not in the method set.
func TestAnEmptyMethodIsTreatedAsGET(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 3,
		Backoff:     resilience.Constant(0),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	u, err := url.Parse("http://x.invalid/")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	req := &http.Request{URL: u, Header: make(http.Header)} // Method deliberately empty

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (an empty method is a GET and GET is retried)", got)
	}
}

// nonReplayable is a body http.NewRequest cannot build a GetBody for.
type nonReplayable struct{ r io.Reader }

func (o *nonReplayable) Read(p []byte) (int, error) { return o.r.Read(p) }

// Spec 3.1. A request whose body cannot be rebuilt must be sent ONCE. Retrying
// it would transmit an empty body on every attempt after the first -- a
// well-formed request carrying nothing, with no error anywhere.
func TestARequestWithANonReplayableBodyIsNotRetried(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	var bodies []string

	rt := resilience.Retry(resilience.Policy{
		MaxAttempts:  3,
		Backoff:      resilience.Constant(0),
		RetryMethods: []string{http.MethodPost}, // opted in, so only replayability can stop it
	})(rtFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		return respond(http.StatusServiceUnavailable), nil
	}))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://x.invalid/", &nonReplayable{strings.NewReader("payload")})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if req.GetBody != nil {
		t.Fatal("precondition failed: GetBody should be nil for an opaque reader")
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (body cannot be replayed)", got)
	}
	if len(bodies) != 1 || bodies[0] != "payload" {
		t.Errorf("bodies = %q, want exactly one %q", bodies, "payload")
	}
}

// Spec 3.1, the other half: when the body CAN be rebuilt, every attempt must
// send the same bytes.
func TestAReplayableBodyIsResentIntact(t *testing.T) {
	t.Parallel()
	var bodies []string

	rt := resilience.Retry(resilience.Policy{
		MaxAttempts:  3,
		Backoff:      resilience.Constant(0),
		RetryMethods: []string{http.MethodPost},
	})(rtFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		return respond(http.StatusServiceUnavailable), nil
	}))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://x.invalid/", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	want := []string{"payload", "payload", "payload"}
	if !slices.Equal(bodies, want) {
		t.Errorf("bodies = %q, want %q -- a retried attempt sent the wrong bytes", bodies, want)
	}
}

// closeTracker records whether a discarded response was drained before close.
type closeTracker struct {
	io.Reader
	readToEOF *atomic.Bool
	closed    *atomic.Bool
}

func (c *closeTracker) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	if err == io.EOF {
		c.readToEOF.Store(true)
	}
	return n, err
}

func (c *closeTracker) Close() error {
	c.closed.Store(true)
	return nil
}

// Spec 3.3. A response the retry loop throws away must be drained to EOF, or
// its connection is not returned to the pool and every attempt costs a fresh
// TCP handshake -- defeating httpclient's MaxIdleConnsPerHost default.
func TestADiscardedResponseIsDrainedAndClosed(t *testing.T) {
	t.Parallel()
	var drained, closed atomic.Bool

	first := true
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 2,
		Backoff:     resilience.Constant(0),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		if first {
			first = false
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body: &closeTracker{
					Reader:    strings.NewReader("an error page"),
					readToEOF: &drained,
					closed:    &closed,
				},
			}, nil
		}
		return respond(http.StatusOK), nil
	}))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if !drained.Load() {
		t.Error("the discarded response was not drained to EOF; its connection will not be reused")
	}
	if !closed.Load() {
		t.Error("the discarded response was not closed")
	}
}

// The returned response must NOT be drained -- the caller still needs it.
// This covers only an unconditional-drain mutation (draining every response,
// including the one handed back): the round-tripper here succeeds on the
// first attempt, so the loop returns before ever reaching the canWait/drain
// code, and this test cannot detect their relative order. That ordering
// hazard is covered separately by
// TestTheReturnedResponseIsNotDrainedWhenTheDeadlineIsExhausted.
func TestAFirstAttemptSuccessIsReturnedUnconsumed(t *testing.T) {
	t.Parallel()
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 2,
		Backoff:     resilience.Constant(0),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK), nil
	}))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the returned body: %v", err)
	}
	if string(b) != "body" {
		t.Errorf("returned body = %q, want %q -- the retry loop consumed it", b, "body")
	}
}

// Spec 3.1, the failure half: if GetBody itself fails on a later attempt (the
// body source became unreadable between attempts -- a temp file removed, a
// stream that errors on re-open), the retry loop must surface that error
// rather than sending a broken request or panicking.
func TestARequestWhoseBodyCannotBeRebuiltReturnsTheRebuildError(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	boom := errors.New("body source gone")

	rt := resilience.Retry(resilience.Policy{
		MaxAttempts:  3,
		Backoff:      resilience.Constant(0),
		RetryMethods: []string{http.MethodPost},
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://x.invalid/", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.GetBody = func() (io.ReadCloser, error) { return nil, boom }

	_, err = rt.RoundTrip(req)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (first attempt uses the original body, not GetBody)", got)
	}
}

// This is the counterpart to TestAFirstAttemptSuccessIsReturnedUnconsumed: a
// retryable first response with a deadline too tight to retry into, so the
// loop returns it via the canWait early-exit rather than the RetryIf
// early-exit. If drain ran before the canWait check, the response handed
// back here would already be closed and empty -- the ordering hazard the
// code comment warns about.
func TestTheReturnedResponseIsNotDrainedWhenTheDeadlineIsExhausted(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 5,
		Backoff:     resilience.Constant(10 * time.Second),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://x.invalid/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (deadline too tight to retry)", got)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the returned body: %v", err)
	}
	if string(b) != "body" {
		t.Errorf("returned body = %q, want %q -- draining ran before the deadline check consumed it", b, "body")
	}
}

// Carried finding from Task 2's review: the attempt cap and RetryIf must be
// checked BEFORE the backoff is computed. A Backoff that records the attempts
// it is asked about pins this -- it must never be asked about the final
// attempt, since there is no wait after it.
func TestBackoffIsNotComputedAfterTheFinalAttempt(t *testing.T) {
	t.Parallel()
	var asked []int
	recordingBackoff := func(attempt int) time.Duration {
		asked = append(asked, attempt)
		return 0
	}

	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 3,
		Backoff:     recordingBackoff,
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusServiceUnavailable), nil
	}))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	want := []int{1, 2}
	if !slices.Equal(asked, want) {
		t.Errorf("Backoff asked about attempts %v, want %v -- it must not be consulted after the final attempt", asked, want)
	}
}

func TestRetryAfterOverridesTheBackoffPolicy(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 2,
		Backoff:     resilience.Constant(5 * time.Second), // would blow the test budget
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		r := respond(http.StatusServiceUnavailable)
		r.Header.Set("Retry-After", "0")
		return r, nil
	}))

	start := time.Now()
	resp, err := rt.RoundTrip(get(t))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	if got := calls.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
	if elapsed > time.Second {
		t.Errorf("took %v; Retry-After: 0 did not override the 5s backoff", elapsed)
	}
}

// An explicit MaxAttempts of 1 means "no retries" and must be respected
// exactly, not treated as unset the way 0 (and below) is.
func TestMaxAttemptsOfOneMeansNoRetries(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 1,
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return respond(http.StatusServiceUnavailable), nil
	}))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 -- an explicit MaxAttempts: 1 must not be overridden to the default", got)
	}
}

// One Retry middleware, many goroutines. Retry(p) normalises p once at
// construction and closes over the result, and every request through a client
// shares that one closure -- so the shared state is real even though nothing
// currently writes to it. Without a test that drives one middleware from
// several goroutines at once, -race has nothing to watch, and the -count=10
// gate conventions.md §5 applies to this module observes an empty suite: two
// halves of one protection, each worthless alone.
//
// The methods are mixed on purpose. A GET reads p.RetryMethods, p.RetryIf,
// p.Backoff and the Retry-After path on every one of its three attempts,
// while a POST takes the ineligible early return -- so the concurrent reads
// cover both branches of eligible rather than one hot loop.
func TestOneRetryMiddlewareIsSafeUnderConcurrentUse(t *testing.T) {
	t.Parallel()
	const goroutines = 64
	const perGoroutine = 8

	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 3,
		Backoff:     resilience.Constant(0),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		r := respond(http.StatusServiceUnavailable)
		r.Header.Set("Retry-After", "0")
		return r, nil
	}))

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			method := http.MethodGet
			if i%2 == 1 {
				method = http.MethodPost
			}
			for j := 0; j < perGoroutine; j++ {
				req, err := http.NewRequestWithContext(context.Background(), method, "http://x.invalid/", nil)
				if err != nil {
					t.Errorf("NewRequest: %v", err)
					return
				}
				resp, err := rt.RoundTrip(req)
				if err != nil {
					t.Errorf("RoundTrip: %v", err)
					return
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusServiceUnavailable {
					t.Errorf("StatusCode = %d, want 503", resp.StatusCode)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	// Half the goroutines send GETs (3 attempts each) and half send POSTs
	// (1 attempt each), so the policy the shared closure holds is asserted,
	// not just the absence of a race report.
	want := int64(goroutines / 2 * perGoroutine * (3 + 1))
	if got := calls.Load(); got != want {
		t.Errorf("attempts = %d, want %d -- the shared policy did not apply uniformly across goroutines", got, want)
	}
}

// A hostile or broken upstream must not be able to park the caller for a day
// with one header. Retry-After outranks the policy backoff, but only up to
// maxRetryAfter; past that the policy backoff is used instead.
//
// The RoundTrip runs in a goroutine behind a select rather than being timed
// inline: without the cap this call sleeps for 24 hours, and a test that
// hangs for go test's 10-minute default instead of failing is the failure
// mode conventions.md §5 forbids. Cancelling the context releases the
// sleeping goroutine on the way out.
func TestAnExcessiveRetryAfterDoesNotParkTheCaller(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rt := resilience.Retry(resilience.Policy{
		MaxAttempts: 2,
		Backoff:     resilience.Constant(0),
	})(rtFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		r := respond(http.StatusServiceUnavailable)
		r.Header.Set("Retry-After", "86400") // one day
		return r, nil
	}))

	ctx, cancel := context.WithCancel(context.Background()) // no deadline: canWait cannot help
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://x.invalid/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	done := make(chan *http.Response, 1)
	go func() {
		resp, _ := rt.RoundTrip(req)
		done <- resp
	}()

	select {
	case resp := <-done:
		if resp != nil {
			resp.Body.Close()
		}
		if got := calls.Load(); got != 2 {
			t.Errorf("attempts = %d, want 2", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Retry-After: 86400 parked the caller; an unbounded header is a denial of service against the client (maxRetryAfter)")
	}
}

// The cap's literal value, pinned at its exact boundary and without sleeping
// for thirty seconds.
//
// The trick is canWait: with a 50ms deadline on the request, a delay the code
// actually adopted is one it cannot afford, so the loop returns the first
// response immediately instead of waiting. So "was the header honoured?"
// reads out as an attempt count, in microseconds. At exactly maxRetryAfter
// the header wins and there is one attempt; one second past it, the policy's
// zero backoff wins and there are two.
func TestTheRetryAfterCapIsExactlyThirtySeconds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		header       string
		wantAttempts int64
	}{
		{"30", 1}, // exactly the cap: honoured, and too long for the deadline
		{"31", 2}, // one second over: ignored, policy backoff of 0 applies
	} {
		var calls atomic.Int64
		rt := resilience.Retry(resilience.Policy{
			MaxAttempts: 2,
			Backoff:     resilience.Constant(0),
		})(rtFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			r := respond(http.StatusServiceUnavailable)
			r.Header.Set("Retry-After", tc.header)
			return r, nil
		}))

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://x.invalid/", nil)
		if err != nil {
			cancel()
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			cancel()
			t.Fatalf("Retry-After %s: RoundTrip: %v", tc.header, err)
		}
		resp.Body.Close()
		cancel()

		if got := calls.Load(); got != tc.wantAttempts {
			t.Errorf("Retry-After: %s -- attempts = %d, want %d (the cap is not exactly %v)", tc.header, got, tc.wantAttempts, 30*time.Second)
		}
	}
}
