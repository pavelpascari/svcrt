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

	// Bounded, not a bare receive: a breaker that admits no probe at all never
	// closes started, and an unbounded wait would hang the whole test binary
	// instead of failing this one test.
	select {
	case <-started: // the probe is inside the transport, holding the half-open slot
	case <-time.After(5 * time.Second):
		t.Fatal("no probe was admitted after the cooldown; nothing holds the half-open slot")
	}
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

func failing() http.RoundTripper {
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: http.NoBody, Request: r}, nil
	})
}

// TestCooldownBoundaryIsExact: no probe one nanosecond early, a probe exactly
// on the boundary. "Roughly after the cooldown" is not a contract anyone can
// rely on, and a >= / > slip is invisible without both halves.
func TestCooldownBoundaryIsExact(t *testing.T) {
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

	clk.advance(time.Minute - time.Nanosecond)
	if _, err := rt.RoundTrip(newReq(t)); !errors.Is(err, ErrOpen) {
		t.Fatalf("a probe was admitted 1ns early: %v", err)
	}

	clk.advance(time.Nanosecond)
	// Asserted unconditionally, NOT inside an `if !errors.Is(err, ErrOpen)`.
	// Nested, the boundary half of this test is vacuous in exactly the
	// direction it exists to catch: a `<=` slip refuses the probe, the guard
	// is false, the body never runs and the test passes. Verified by running
	// that mutation -- every other probe test failed and this one did not.
	resp, err = rt.RoundTrip(newReq(t))
	if errors.Is(err, ErrOpen) {
		t.Fatal("no probe admitted exactly at the boundary")
	}
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream saw %d requests, want 2 (the trip and the boundary probe)", got)
	}
}

// TestSuccessfulProbeClosesTheCircuit.
func TestSuccessfulProbeClosesTheCircuit(t *testing.T) {
	clk := newClock()
	var healthy atomic.Bool

	rt := Breaker(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute, now: clk.now})(
		roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			code := http.StatusInternalServerError
			if healthy.Load() {
				code = http.StatusOK
			}
			return &http.Response{StatusCode: code, Body: http.NoBody, Request: r}, nil
		}))

	resp, err := rt.RoundTrip(newReq(t)) // trips
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	healthy.Store(true)
	clk.advance(time.Minute)

	resp, err = rt.RoundTrip(newReq(t)) // the probe, succeeds
	if err != nil {
		t.Fatalf("probe was refused: %v", err)
	}
	resp.Body.Close()

	// Circuit is closed: a further request passes with no clock advance.
	resp, err = rt.RoundTrip(newReq(t))
	if err != nil {
		t.Fatalf("circuit did not close after a successful probe: %v", err)
	}
	resp.Body.Close()
}

// TestFailedProbeReopensForAnotherFullCooldown: a failed probe must restart
// the clock, not leave the circuit admitting a probe per request.
func TestFailedProbeReopensForAnotherFullCooldown(t *testing.T) {
	clk := newClock()
	// Threshold 3, not 1: at 1 the breaker is already at its threshold for
	// every subsequent failure, so the test could not tell "a failed probe
	// re-opens" from "the count happens to be over the line anyway".
	rt := Breaker(BreakerPolicy{FailureThreshold: 3, Cooldown: time.Minute, now: clk.now})(failing())

	for i := range 3 { // trips
		resp, err := rt.RoundTrip(newReq(t))
		if err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
		resp.Body.Close()
	}

	clk.advance(time.Minute)
	resp, err := rt.RoundTrip(newReq(t)) // probe, fails
	if err != nil {
		t.Fatalf("probe refused: %v", err)
	}
	resp.Body.Close()

	// Immediately after: still open.
	if _, err := rt.RoundTrip(newReq(t)); !errors.Is(err, ErrOpen) {
		t.Fatalf("a failed probe did not re-open the circuit: %v", err)
	}
	// Half a cooldown later: still open.
	clk.advance(5 * time.Second)
	if _, err := rt.RoundTrip(newReq(t)); !errors.Is(err, ErrOpen) {
		t.Fatalf("cooldown did not restart after the failed probe: %v", err)
	}
}

// TestOnlyOneProbeIsAdmittedConcurrently is the concurrency assertion that
// matters: many goroutines arriving at once, exactly one probe through. This
// is the shape that produced R1's dropped-fatal race behind 100% coverage.
func TestOnlyOneProbeIsAdmittedConcurrently(t *testing.T) {
	clk := newClock()
	var inFlight, maxInFlight, admitted atomic.Int32
	release := make(chan struct{})

	rt := Breaker(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute, now: clk.now})(
		roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			admitted.Add(1)
			n := inFlight.Add(1)
			for {
				m := maxInFlight.Load()
				if n <= m || maxInFlight.CompareAndSwap(m, n) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
			return &http.Response{StatusCode: 500, Body: http.NoBody, Request: r}, nil
		}))

	// Trip it, letting the first request through immediately.
	close(release)
	resp, err := rt.RoundTrip(newReq(t))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Re-arm the gate so the probe blocks inside the transport.
	release = make(chan struct{})
	admitted.Store(0)
	clk.advance(time.Minute)

	var wg sync.WaitGroup
	var opens atomic.Int32
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := rt.RoundTrip(newReq(t))
			if errors.Is(err, ErrOpen) {
				opens.Add(1)
				return
			}
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	// Give the goroutines time to pile up against the open circuit, then let
	// the single probe finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := admitted.Load(); got != 1 {
		t.Fatalf("%d requests reached the upstream during one cooldown, want exactly 1", got)
	}
	if got := maxInFlight.Load(); got > 1 {
		t.Fatalf("%d concurrent probes in flight, want at most 1", got)
	}
	if got := opens.Load(); got != 49 {
		t.Errorf("%d requests refused, want 49", got)
	}
}

// TestBreakerIsSafeUnderConcurrentLoad drives the whole state machine from
// many goroutines. It asserts no panic and no deadlock; -race asserts the rest.
func TestBreakerIsSafeUnderConcurrentLoad(t *testing.T) {
	clk := newClock()
	var flip atomic.Bool
	rt := Breaker(BreakerPolicy{FailureThreshold: 3, Cooldown: time.Millisecond, now: clk.now})(
		roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			code := http.StatusOK
			if flip.Load() {
				code = http.StatusInternalServerError
			}
			return &http.Response{StatusCode: code, Body: http.NoBody, Request: r}, nil
		}))

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 50 {
				flip.Store((i+j)%3 == 0)
				if j%10 == 0 {
					clk.advance(time.Millisecond)
				}
				resp, err := rt.RoundTrip(newReq(t))
				if err == nil {
					resp.Body.Close()
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestNoReachableStateIsHalfOpenWithoutAFullFailureCount is executed evidence
// for an equivalence claim, not a behaviour test.
//
// Dropping `b.state == stateHalfOpen ||` from record's re-open condition
// leaves the whole suite green. That looks like a decorative test until you
// ask why: `fails` is only ever reset by the non-tripping branch, and that
// same branch sets the state to closed -- so every reachable half-open state
// already carries fails >= FailureThreshold, and the clause it is guarded by
// can never be the deciding term. The mutant is equivalent.
//
// "Algebraically sound" is not evidence in this repo (R3 shipped one that was
// sound and false), so the invariant is model-checked rather than argued:
// breadth-first over every state the four state-machine operations can reach,
// with fails saturated at threshold+1 because nothing reads it except
// `fails >= threshold`, and with "time passes" as an extra edge out of every
// state.
//
// If a future change resets fails when the circuit opens -- a reasonable
// change -- this test starts failing, and that is the signal that the clause
// has become load-bearing and needs a behaviour test of its own.
func TestNoReachableStateIsHalfOpenWithoutAFullFailureCount(t *testing.T) {
	const threshold = 3
	const cooldown = time.Minute
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	type abstract struct {
		state   breakerState
		fails   int // saturated at threshold+1
		probing bool
		elapsed bool // Cooldown has passed since openedAt
	}

	ops := []func(*breaker){
		func(b *breaker) { _, _ = b.allow() },
		func(b *breaker) { b.record(true) },
		func(b *breaker) { b.record(false) },
		func(b *breaker) { b.releaseProbe() },
	}

	load := func(a abstract) (*breaker, *fakeClock) {
		clk := &fakeClock{t: base}
		if a.elapsed {
			clk.t = base.Add(cooldown)
		}
		return &breaker{
			p:        BreakerPolicy{FailureThreshold: threshold, Cooldown: cooldown, now: clk.now}.withDefaults(),
			state:    a.state,
			fails:    a.fails,
			probing:  a.probing,
			openedAt: base,
		}, clk
	}

	save := func(b *breaker, clk *fakeClock) abstract {
		return abstract{
			state:   b.state,
			fails:   min(b.fails, threshold+1),
			probing: b.probing,
			elapsed: !clk.now().Before(b.openedAt.Add(cooldown)),
		}
	}

	start := abstract{state: stateClosed}
	seen := map[abstract]bool{start: true}
	queue := []abstract{start}
	var halfOpenStates int

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.state == stateHalfOpen {
			halfOpenStates++
			if cur.fails < threshold {
				t.Fatalf("reachable state %+v is half-open with fails < threshold: "+
					"the stateHalfOpen clause in record is load-bearing after all", cur)
			}
		}
		for _, op := range ops {
			b, clk := load(cur)
			op(b)
			reached := save(b, clk)
			// Time may or may not pass before the next operation.
			for _, elapsed := range []bool{false, true} {
				next := reached
				next.elapsed = next.elapsed || elapsed
				if !seen[next] {
					seen[next] = true
					queue = append(queue, next)
				}
			}
		}
	}

	// Without this the whole test is vacuous: a search that never reaches
	// half-open proves nothing about half-open.
	if halfOpenStates == 0 {
		t.Fatal("the search never reached a half-open state; it proves nothing")
	}
	if len(seen) < 8 {
		t.Fatalf("only %d states reached; the search collapsed", len(seen))
	}
}
