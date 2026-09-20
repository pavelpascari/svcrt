package resilience_test

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/pavelpascari/svcrt/resilience"
)

// scripted is a stand-in for the upstream. Every constructor in this package
// is middleware over http.RoundTripper -- stdlib vocabulary -- so an example
// needs no server and no client library to show what they do.
type scripted struct {
	codes []int
	calls int
}

func (s *scripted) RoundTrip(r *http.Request) (*http.Response, error) {
	code := s.codes[min(s.calls, len(s.codes)-1)]
	s.calls++
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Body:       http.NoBody,
		Request:    r,
	}, nil
}

// ExampleRetry re-sends a failed request. 503 is in the default retry set and
// GET is in the default method set, so the policy here only says how many
// attempts and how long to wait between them.
func ExampleRetry() {
	upstream := &scripted{codes: []int{503, 503, 200}}

	// Constant keeps the example fast. The default is a jittered exponential
	// from 100ms to 2s, which is what a service should use: without jitter,
	// every client that failed at the same instant retries at the same
	// instant and the recovering dependency is knocked over again.
	c := &http.Client{Transport: resilience.Retry(resilience.Policy{
		MaxAttempts: 3,
		Backoff:     resilience.Constant(time.Millisecond),
	})(upstream)}

	resp, err := c.Get("http://orders.internal/v1/orders")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	resp.Body.Close()

	fmt.Println("status:", resp.StatusCode)
	fmt.Println("upstream calls:", upstream.calls)

	// Output:
	// status: 200
	// upstream calls: 3
}

// ExampleBreaker stops sending to a dependency that is failing. Compose it
// OUTSIDE Retry so it counts logical calls rather than attempts.
func ExampleBreaker() {
	upstream := &scripted{codes: []int{500}} // down, and staying down

	c := &http.Client{Transport: resilience.Breaker(resilience.BreakerPolicy{
		FailureThreshold: 2,
		Cooldown:         time.Minute,
	})(upstream)}

	for i := 1; i <= 3; i++ {
		resp, err := c.Get("http://orders.internal/v1/orders")
		switch {
		case errors.Is(err, resilience.ErrOpen):
			fmt.Printf("call %d: circuit open, upstream not called\n", i)
		case err != nil:
			fmt.Printf("call %d: %v\n", i, err)
		default:
			resp.Body.Close()
			fmt.Printf("call %d: %d\n", i, resp.StatusCode)
		}
	}
	fmt.Println("upstream calls:", upstream.calls)

	// Output:
	// call 1: 500
	// call 2: 500
	// call 3: circuit open, upstream not called
	// upstream calls: 2
}

// wedged never answers. It returns only once the attempt's context is
// cancelled, so the example below depends on the timeout firing rather than on
// how fast the machine is.
type wedged struct{ calls int }

func (w *wedged) RoundTrip(r *http.Request) (*http.Response, error) {
	w.calls++
	<-r.Context().Done()
	return nil, r.Context().Err()
}

// ExampleTimeout bounds one attempt rather than the whole sequence. Which of
// the two you mean is expressed by the composition order, which is why this is
// middleware rather than a field on Policy:
//
//	Retry(p)(Timeout(d)(next))  // d bounds EACH attempt
//	Timeout(d)(Retry(p)(next))  // d bounds the whole retry sequence
//
// This package folds nothing for you; httpclient.Chain composes middleware of
// this shape when there are several.
func ExampleTimeout() {
	upstream := &wedged{}

	perAttempt := resilience.Retry(resilience.Policy{
		MaxAttempts: 3,
		Backoff:     resilience.Constant(time.Millisecond),
	})(resilience.Timeout(10 * time.Millisecond)(upstream))

	c := &http.Client{Transport: perAttempt}

	_, err := c.Get("http://orders.internal/v1/orders")
	fmt.Println("failed:", err != nil)
	fmt.Println("attempts:", upstream.calls)

	// Output:
	// failed: true
	// attempts: 3
}

// ExampleExponential shows the default backoff shape, and Jitter, which is
// what keeps a fleet of clients from retrying in one synchronised wave.
func ExampleExponential() {
	b := resilience.Exponential(100*time.Millisecond, 2*time.Second)
	for attempt := 1; attempt <= 6; attempt++ {
		fmt.Printf("attempt %d: %s\n", attempt, b(attempt))
	}

	// Jitter randomises into [0, b(attempt)], so only the bound is printable.
	j := resilience.Jitter(b)
	fmt.Println("jittered within bound:", j(3) <= b(3))

	// Output:
	// attempt 1: 100ms
	// attempt 2: 200ms
	// attempt 3: 400ms
	// attempt 4: 800ms
	// attempt 5: 1.6s
	// attempt 6: 2s
	// jittered within bound: true
}
