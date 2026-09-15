package resilience

import (
	"errors"
	"net/http"
	"sync"
	"time"
)

// ErrOpen is returned instead of a response when the breaker is refusing
// requests. Match it with errors.Is.
var ErrOpen = errors.New("resilience: circuit breaker is open")

const (
	defaultFailureThreshold = 5
	defaultCooldown         = 30 * time.Second
)

// BreakerPolicy configures Breaker.
//
// It is a separate type from Policy rather than more fields on it: they share
// nothing, and one struct whose halves apply to different middlewares is an
// option that silently does nothing when set on the wrong one.
type BreakerPolicy struct {
	// FailureThreshold is the number of CONSECUTIVE tripping results that
	// opens the circuit. 0 means 5. Any non-tripping result resets the count.
	FailureThreshold int

	// Cooldown is how long the circuit stays open before admitting a probe.
	// 0 means 30s.
	Cooldown time.Duration

	// TripIf decides whether a result counts as a failure. nil means any
	// transport error or any 5xx -- deliberately NOT the same predicate as
	// Policy.RetryIf. See Breaker.
	//
	// resp is nil whenever err is non-nil; check err first.
	TripIf func(*http.Response, error) bool

	// now is a clock seam for tests. nil means time.Now. Unexported because a
	// caller has no reason to replace the clock, and the cooldown boundary is
	// exact enough that testing it against the wall clock would mean either
	// real sleeps or a weaker assertion than the contract.
	now func() time.Time
}

func (p BreakerPolicy) withDefaults() BreakerPolicy {
	if p.FailureThreshold <= 0 {
		p.FailureThreshold = defaultFailureThreshold
	}
	if p.Cooldown <= 0 {
		p.Cooldown = defaultCooldown
	}
	if p.TripIf == nil {
		p.TripIf = defaultTripIf
	}
	if p.now == nil {
		p.now = time.Now
	}
	return p
}

// defaultTripIf trips on any transport error and any 5xx.
//
// It is deliberately NOT defaultRetryIf, and the two disagree in both
// directions:
//
//	                     Retry retries   Breaker trips
//	transport error          yes             yes
//	429 Too Many Requests    YES             NO
//	500 Internal Error       NO              YES
//	502 / 503 / 504          yes             yes
//
// A 429 must not trip the circuit. It is flow control, not a broken
// dependency: the upstream is up, responding, and asking for a lower rate.
// Opening on it converts throttling into a self-inflicted outage at exactly
// the moment the upstream wants less load rather than none.
//
// A 500 must trip it even though Retry will not retry one. Retry's reason for
// skipping a 500 is that it usually repeats -- which is the same fact that
// makes it what a breaker is for. Retrying a deterministic failure wastes one
// call; continuing to send traffic into it for minutes wastes all of them.
func defaultTripIf(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return resp.StatusCode >= 500
}

type breakerState int

const (
	stateClosed breakerState = iota
	stateOpen
	stateHalfOpen
)

// breaker is the shared mutable state one Breaker middleware holds.
type breaker struct {
	p BreakerPolicy

	mu       sync.Mutex
	state    breakerState
	fails    int
	openedAt time.Time
	probing  bool
}

// allow reports whether the request may proceed, and whether it is the
// half-open probe. It never blocks on anything but the mutex.
func (b *breaker) allow() (probe bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case stateOpen:
		if b.p.now().Sub(b.openedAt) < b.p.Cooldown {
			return false, ErrOpen
		}
		b.state = stateHalfOpen
		b.probing = true
		return true, nil

	case stateHalfOpen:
		// A probe is already in flight. Rejecting rather than queueing or
		// admitting is the point: admitting them sends a burst at an upstream
		// that has just demonstrated it is unwell.
		if b.probing {
			return false, ErrOpen
		}
		b.probing = true
		return true, nil

	default: // stateClosed
		return false, nil
	}
}

// record folds one result into the state machine.
func (b *breaker) record(tripped bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !tripped {
		b.state = stateClosed
		b.fails = 0
		return
	}

	b.fails++
	// A failed probe re-opens immediately, without waiting for the threshold:
	// the upstream was just asked one question and got it wrong.
	if b.state == stateHalfOpen || b.fails >= b.p.FailureThreshold {
		b.state = stateOpen
		b.openedAt = b.p.now()
	}
}

// releaseProbe clears the in-flight probe flag. Deferred rather than folded
// into record so a panicking transport cannot leave the breaker permanently
// convinced a probe is still running -- which would reject every subsequent
// request forever.
func (b *breaker) releaseProbe() {
	b.mu.Lock()
	b.probing = false
	b.mu.Unlock()
}

// Breaker returns middleware that stops sending requests to a dependency that
// is failing, and periodically checks whether it has recovered.
//
// It trips after FailureThreshold CONSECUTIVE failures, stays open for
// Cooldown, then admits exactly one probe: succeeding closes it, failing
// re-opens it for another Cooldown. While open it returns ErrOpen without
// calling the next transport at all.
//
// Compose it OUTSIDE Retry, so it counts logical calls rather than attempts: a
// request that succeeds on attempt 2 is a success, and one that exhausts its
// retries is a single failure. Inside Retry, one three-attempt burst against a
// briefly flaky upstream would trip a threshold-3 breaker even though the call
// ultimately succeeded. Outside, an open circuit also short-circuits the whole
// retry loop instead of letting it spin.
//
// What it trips on is NOT what Retry retries -- see defaultTripIf.
//
// **One breaker protects one middleware, with no keying by host.** A client
// shared across several upstreams therefore gets ONE circuit for all of them,
// and a single dead host will open it for the healthy ones. Give each upstream
// its own client, which is the recommendation regardless.
//
// **Consecutive counting cannot see a steady error rate.** An upstream failing
// 30% of calls forever will, at the default threshold of 5, almost never
// produce five failures in a row. Supply TripIf if you need a different signal.
func Breaker(p BreakerPolicy) func(http.RoundTripper) http.RoundTripper {
	b := &breaker{p: p.withDefaults()}

	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			probe, err := b.allow()
			if err != nil {
				return nil, err
			}
			if probe {
				defer b.releaseProbe()
			}

			// The mutex is deliberately not held here. Holding it across the
			// round trip would serialise every request through the breaker.
			resp, rtErr := next.RoundTrip(req)

			b.record(b.p.TripIf(resp, rtErr))
			return resp, rtErr
		})
	}
}
