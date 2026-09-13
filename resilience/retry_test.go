package resilience_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
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
