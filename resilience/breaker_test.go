package resilience_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/pavelpascari/svcrt/resilience"
)

// status returns a transport that always answers with the given status.
// rtFunc and respond come from retry_test.go.
func status(code int) http.RoundTripper {
	return rtFunc(func(r *http.Request) (*http.Response, error) {
		resp := respond(code)
		resp.Request = r
		return resp, nil
	})
}

// TestBreakerOpensAtThresholdNotBefore pins both edges. A breaker that opened
// at threshold-1 would reject a caller one failure early; one that opened at
// threshold+1 would let an extra request through. Only asserting "it opens
// eventually" catches neither.
func TestBreakerOpensAtThresholdNotBefore(t *testing.T) {
	rt := resilience.Breaker(resilience.BreakerPolicy{FailureThreshold: 3})(status(500))

	for i := 1; i <= 2; i++ {
		resp, err := rt.RoundTrip(get(t))
		if err != nil {
			t.Fatalf("attempt %d: unexpected error %v", i, err)
		}
		resp.Body.Close()
	}
	// Third failure reaches the upstream and is the one that trips it.
	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatalf("attempt 3 should still reach the upstream: %v", err)
	}
	resp.Body.Close()

	// Fourth is refused.
	if _, err := rt.RoundTrip(get(t)); !errors.Is(err, resilience.ErrOpen) {
		t.Fatalf("attempt 4 error = %v, want ErrOpen", err)
	}
}

// TestBreakerOpenDoesNotCallTheTransport: rejecting must be free. A breaker
// that still dials has saved the upstream nothing, which is the entire point.
func TestBreakerOpenDoesNotCallTheTransport(t *testing.T) {
	var calls int
	rt := resilience.Breaker(resilience.BreakerPolicy{FailureThreshold: 1})(
		rtFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 500, Body: http.NoBody, Request: r}, nil
		}))

	resp, err := rt.RoundTrip(get(t)) // trips it
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if _, err := rt.RoundTrip(get(t)); !errors.Is(err, resilience.ErrOpen) {
		t.Fatalf("error = %v, want ErrOpen", err)
	}
	if calls != 1 {
		t.Fatalf("transport called %d times; the open breaker dialled anyway", calls)
	}
}

// TestBreakerSuccessResetsTheCount is what makes it CONSECUTIVE failures.
// Without the reset this is a lifetime failure counter and every long-lived
// client eventually trips.
func TestBreakerSuccessResetsTheCount(t *testing.T) {
	var fail bool
	rt := resilience.Breaker(resilience.BreakerPolicy{FailureThreshold: 3})(
		rtFunc(func(r *http.Request) (*http.Response, error) {
			code := http.StatusOK
			if fail {
				code = http.StatusInternalServerError
			}
			resp := respond(code)
			resp.Request = r
			return resp, nil
		}))

	// 2 failures, a success, then 2 more failures: never 3 in a row.
	for _, seq := range []bool{true, true, false, true, true} {
		fail = seq
		resp, err := rt.RoundTrip(get(t))
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		resp.Body.Close()
	}

	fail = false
	if _, err := rt.RoundTrip(get(t)); err != nil {
		t.Fatalf("breaker opened despite never seeing 3 consecutive failures: %v", err)
	}
}

// TestBreakerDoesNotTripOnTooManyRequests is the deliberate difference from
// RetryIf, and the one most likely to be "fixed" by someone sharing the two
// predicates. A 429 means the upstream is up and asking for less load; opening
// a circuit on it converts throttling into a self-inflicted outage.
func TestBreakerDoesNotTripOnTooManyRequests(t *testing.T) {
	rt := resilience.Breaker(resilience.BreakerPolicy{FailureThreshold: 2})(
		status(http.StatusTooManyRequests))

	for i := range 5 {
		resp, err := rt.RoundTrip(get(t))
		if err != nil {
			t.Fatalf("request %d: 429 tripped the breaker: %v", i, err)
		}
		resp.Body.Close()
	}
}

// TestBreakerTripsOnInternalServerError: the other half of the same difference.
// Retry deliberately does NOT retry a 500 because it repeats -- which is
// exactly what makes it worth tripping on.
func TestBreakerTripsOnInternalServerError(t *testing.T) {
	rt := resilience.Breaker(resilience.BreakerPolicy{FailureThreshold: 1})(
		status(http.StatusInternalServerError))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if _, err := rt.RoundTrip(get(t)); !errors.Is(err, resilience.ErrOpen) {
		t.Fatalf("a 500 did not trip the breaker: %v", err)
	}
}

// TestBreakerTripsOnTransportError: no response at all is the clearest failure.
func TestBreakerTripsOnTransportError(t *testing.T) {
	boom := errors.New("dial tcp: connection refused")
	rt := resilience.Breaker(resilience.BreakerPolicy{FailureThreshold: 1})(
		rtFunc(func(*http.Request) (*http.Response, error) { return nil, boom }))

	if _, err := rt.RoundTrip(get(t)); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want it returned unchanged", err)
	}
	if _, err := rt.RoundTrip(get(t)); !errors.Is(err, resilience.ErrOpen) {
		t.Fatalf("a transport error did not trip the breaker: %v", err)
	}
}

// TestBreakerTripIfOverridesTheDefault, proven with a status the default
// ignores entirely.
func TestBreakerTripIfOverridesTheDefault(t *testing.T) {
	rt := resilience.Breaker(resilience.BreakerPolicy{
		FailureThreshold: 1,
		TripIf: func(resp *http.Response, err error) bool {
			return err != nil || resp.StatusCode == http.StatusNotFound
		},
	})(status(http.StatusNotFound))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if _, err := rt.RoundTrip(get(t)); !errors.Is(err, resilience.ErrOpen) {
		t.Fatal("TripIf did not override the default predicate")
	}
}

// TestEveryBreakerOptionLandsOnItsOwnDestination covers the struct-literal
// defaulting go-mutesting cannot mutate (carried from R2 and R3).
func TestEveryBreakerOptionLandsOnItsOwnDestination(t *testing.T) {
	var tripIfCalled bool
	rt := resilience.Breaker(resilience.BreakerPolicy{
		FailureThreshold: 2, // observable: opens on the 2nd failure, not the 5th
		TripIf: func(resp *http.Response, err error) bool {
			tripIfCalled = true
			return true
		},
	})(status(http.StatusOK))

	for range 2 {
		resp, err := rt.RoundTrip(get(t))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if !tripIfCalled {
		t.Error("BreakerPolicy.TripIf never reached the breaker")
	}
	if _, err := rt.RoundTrip(get(t)); !errors.Is(err, resilience.ErrOpen) {
		t.Error("BreakerPolicy.FailureThreshold did not reach the breaker")
	}
}
