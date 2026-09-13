// Package resilience provides retry, backoff and per-attempt timeouts as
// middleware over http.RoundTripper.
//
// Every constructor here returns the bare func(http.RoundTripper)
// http.RoundTripper rather than a named type, so the result is assignable to a
// consumer's own middleware type -- httpclient.Middleware, for instance --
// without either module importing the other. Two different named types with
// identical underlying types are not assignable in Go; one side must be
// unnamed, and this is that side.
package resilience

import (
	"math/rand/v2"
	"time"
)

// Backoff reports how long to wait before a given attempt. Attempts are
// 1-based: attempt 1 is the delay after the first failure.
type Backoff func(attempt int) time.Duration

// Constant waits d before every attempt.
func Constant(d time.Duration) Backoff {
	return func(int) time.Duration { return d }
}

// Exponential doubles from base, saturating at max.
//
// The doubling is guarded rather than computed and clamped: doubling a
// time.Duration enough times overflows int64 and wraps negative, which would
// turn a long backoff into no backoff at all.
func Exponential(base, max time.Duration) Backoff {
	return func(attempt int) time.Duration {
		if attempt < 1 {
			attempt = 1
		}
		d := base
		for i := 1; i < attempt; i++ {
			if d >= max/2 {
				return max
			}
			d *= 2
		}
		if d > max {
			return max
		}
		return d
	}
}

// Jitter randomises b's delay into [0, b(attempt)], the "full jitter" policy.
//
// Without it, every client that failed at the same instant retries at the same
// instant, and the retry storm arrives as a single synchronised wave -- which
// is how a recovering dependency gets knocked over again.
//
// rand.N from math/rand/v2 is goroutine-safe and needs no seeding. math/rand's
// global source would be shared mutable state on the request path.
func Jitter(b Backoff) Backoff {
	return func(attempt int) time.Duration {
		d := b(attempt)
		if d <= 0 {
			return 0
		}
		return time.Duration(rand.N(int64(d) + 1))
	}
}
