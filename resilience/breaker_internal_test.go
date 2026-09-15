package resilience

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is the seam that makes the cooldown boundary testable exactly.
// Against the wall clock this would mean real sleeps -- slow, and flaky under
// -count=10 -- or an assertion weaker than the contract.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newReq builds a request for the internal tests. retryafter_test.go,
// retry_internal_test.go and drain_internal_test.go are also `package
// resilience` and none of them declares a request helper, so this is the
// first one in the package.
func newReq(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "http://upstream.invalid/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestProbeIsAdmittedAfterTheCooldown is the open -> half-open edge: the
// circuit does not stay open forever, it re-checks. Task 2 pins the boundary
// exactly; this pins that the transition happens at all.
func TestProbeIsAdmittedAfterTheCooldown(t *testing.T) {
	clk := newClock()
	var calls atomic.Int32
	rt := Breaker(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute, now: clk.now})(
		roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 500, Body: http.NoBody, Request: r}, nil
		}))

	resp, err := rt.RoundTrip(newReq(t)) // trips
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if _, err := rt.RoundTrip(newReq(t)); !errors.Is(err, ErrOpen) {
		t.Fatalf("still inside the cooldown: err = %v, want ErrOpen", err)
	}

	clk.advance(time.Minute)
	resp, err = rt.RoundTrip(newReq(t)) // the probe reaches the upstream
	if err != nil {
		t.Fatalf("no probe admitted after the cooldown: %v", err)
	}
	resp.Body.Close()

	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream saw %d requests, want 2 (the trip and the probe)", got)
	}
}

// TestARequestArrivingDuringTheProbeIsRefused pins the half-open contract:
// exactly one request in flight. The probe is parked inside the transport, so
// the second request observes the in-flight window deterministically rather
// than by timing.
func TestARequestArrivingDuringTheProbeIsRefused(t *testing.T) {
	clk := newClock()
	started := make(chan struct{})
	release := make(chan struct{})
	var blocking, parked atomic.Bool

	rt := Breaker(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute, now: clk.now})(
		roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			// Exactly one call parks. A breaker that wrongly admits a second
			// request must then fail this test's assertion rather than
			// deadlock on release or panic on a double close -- a mutation
			// that hangs the binary reports nothing.
			if blocking.Load() && parked.CompareAndSwap(false, true) {
				close(started)
				<-release
			}
			return &http.Response{StatusCode: 500, Body: http.NoBody, Request: r}, nil
		}))

	resp, err := rt.RoundTrip(newReq(t)) // trips
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	blocking.Store(true)
	clk.advance(time.Minute)

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := rt.RoundTrip(newReq(t)) // the probe
		if err == nil {
			resp.Body.Close()
		}
	}()

	<-started // the probe is inside the transport, holding the half-open slot
	if _, err := rt.RoundTrip(newReq(t)); !errors.Is(err, ErrOpen) {
		t.Errorf("a request arriving during the probe: err = %v, want ErrOpen", err)
	}
	close(release)
	<-done
}

// TestAPanickingTransportDoesNotStrandTheProbe is what makes the `defer
// b.releaseProbe()` load-bearing rather than decorative. Folding the release
// into record instead is an equivalent refactor for every result the
// transport can RETURN -- but a transport that panics never reaches record,
// and the breaker would then be permanently convinced a probe is in flight
// and refuse every subsequent request forever.
//
// It also reaches the only state the rest of the suite cannot: half-open with
// no probe outstanding.
func TestAPanickingTransportDoesNotStrandTheProbe(t *testing.T) {
	clk := newClock()
	var boom atomic.Bool

	rt := Breaker(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute, now: clk.now})(
		roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if boom.Load() {
				panic("transport exploded")
			}
			return &http.Response{StatusCode: 500, Body: http.NoBody, Request: r}, nil
		}))

	resp, err := rt.RoundTrip(newReq(t)) // trips
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	boom.Store(true)
	clk.advance(time.Minute)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the transport panic did not propagate to the caller")
			}
		}()
		_, _ = rt.RoundTrip(newReq(t)) // the probe, panics
	}()

	// The clock has NOT advanced again, so nothing but a released probe slot
	// can let this through.
	boom.Store(false)
	resp, err = rt.RoundTrip(newReq(t))
	if err != nil {
		t.Fatalf("the breaker is still convinced a probe is in flight: %v", err)
	}
	resp.Body.Close()
}

// TestDefaultsApply pins the documented zero-value behaviour, which
// go-mutesting cannot reach through a struct literal.
func TestDefaultsApply(t *testing.T) {
	p := BreakerPolicy{}.withDefaults()
	if p.FailureThreshold != defaultFailureThreshold {
		t.Errorf("FailureThreshold = %d, want %d", p.FailureThreshold, defaultFailureThreshold)
	}
	if p.Cooldown != defaultCooldown {
		t.Errorf("Cooldown = %v, want %v", p.Cooldown, defaultCooldown)
	}
	if p.TripIf == nil {
		t.Error("TripIf was not defaulted")
	}
	if p.now == nil {
		t.Error("now was not defaulted")
	}
}
