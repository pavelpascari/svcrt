package main

import (
	"context"
	"errors"
	"testing"

	"github.com/pavelpascari/svcrt/resilience"
	"github.com/pavelpascari/svcrt/testkit"
)

// TestAcceptanceBreakerStopsCallingADeadUpstream drives the REAL buildStack
// wiring, not a stack assembled by the test, because the thing being asserted
// is that the breaker is in the production chain at all.
//
// It exists because removing the breaker from that chain used to change
// nothing: the entire exemplar suite passed with DisableBreaker set. Retry has
// had an equivalent guard since R3 -- disabling it fails this suite -- and the
// breaker shipped without one.
//
// The upstream answers 500, which is the status that separates the two
// middlewares most cleanly: resilience's default RetryIf deliberately does NOT
// retry a 500, while the breaker's default TripIf deliberately DOES trip on
// one. So each Quote is exactly one upstream request, and the count is a
// direct read of when the circuit opened.
func TestAcceptanceBreakerStopsCallingADeadUpstream(t *testing.T) {
	upstream := testkit.Upstream(t, testkit.Status(500))

	s := newStack(t, 0, upstream.URL())

	const calls = 8
	var lastErr error
	for range calls {
		_, lastErr = s.pricing.Quote(context.Background(), "sku-1")
	}

	if got := upstream.Requests(); got != breakerThreshold {
		t.Fatalf("upstream saw %d requests across %d calls, want %d: the breaker is not in the production chain, or its threshold did not reach it",
			got, calls, breakerThreshold)
	}

	// The caller can act on it: the sentinel survives both http.Client's
	// *url.Error wrapper and Quote's own fmt.Errorf.
	if !errors.Is(lastErr, resilience.ErrOpen) {
		t.Fatalf("last error = %v, want it to unwrap to resilience.ErrOpen", lastErr)
	}
}

// TestAcceptanceBreakerLeavesAHealthyUpstreamAlone is the other half. A guard
// that only proves the circuit opens would pass just as well if it opened
// immediately and never closed.
func TestAcceptanceBreakerLeavesAHealthyUpstreamAlone(t *testing.T) {
	upstream := testkit.Upstream(t, testkit.JSON(200, `{"amount_minor":1250}`))

	s := newStack(t, 0, upstream.URL())

	const calls = 8
	for i := range calls {
		amount, err := s.pricing.Quote(context.Background(), "sku-1")
		if err != nil {
			t.Fatalf("call %d failed against a healthy upstream: %v", i, err)
		}
		if amount != 1250 {
			t.Fatalf("call %d amount = %d, want 1250", i, amount)
		}
	}
	if got := upstream.Requests(); got != calls {
		t.Fatalf("upstream saw %d of %d requests; the breaker opened on a healthy upstream", got, calls)
	}
}
