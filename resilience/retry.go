package resilience

import (
	"context"
	"net/http"
	"slices"
	"time"
)

// roundTripperFunc adapts a function to http.RoundTripper. Unexported because
// this module's exported surface is middleware, not adapters -- a consumer that
// needs one has httpclient.RoundTripperFunc.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const defaultMaxAttempts = 3

// The six methods RFC 9110 defines as idempotent. POST and PATCH are absent on
// purpose: a lost response does not mean the request did not happen, so
// retrying one duplicates its effect.
var defaultRetryMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPut,
	http.MethodDelete, http.MethodOptions, http.MethodTrace,
}

// Policy configures Retry. Every zero-valued field takes a documented default.
type Policy struct {
	// MaxAttempts counts the initial try. 0 means defaultMaxAttempts.
	MaxAttempts int

	// Backoff is the delay before each retry. nil means a jittered
	// exponential from 100ms to 2s.
	Backoff Backoff

	// RetryMethods are the HTTP methods eligible for retry. nil means the six
	// idempotent ones. Set it explicitly to opt a POST in when the endpoint is
	// idempotent by key.
	RetryMethods []string

	// RetryIf decides whether a result is worth retrying. nil means any
	// transport error, plus 429, 502, 503 and 504.
	RetryIf func(*http.Response, error) bool
}

// defaultRetryIf retries transport errors and the four statuses that mean
// "try again", not "your request is wrong".
//
// 500 is deliberately absent. It usually means a genuine server-side bug,
// which repeats -- retrying converts one failure into three and adds load to a
// dependency that is already failing. A caller who knows their 500s are
// transient sets RetryIf.
func defaultRetryIf(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

func (p Policy) withDefaults() Policy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = defaultMaxAttempts
	}
	if p.Backoff == nil {
		p.Backoff = Jitter(Exponential(100*time.Millisecond, 2*time.Second))
	}
	if p.RetryMethods == nil {
		p.RetryMethods = defaultRetryMethods
	}
	if p.RetryIf == nil {
		p.RetryIf = defaultRetryIf
	}
	return p
}

// Retry returns middleware that re-sends a failed request according to p.
//
// The return type is the bare func(http.RoundTripper) http.RoundTripper rather
// than a named type, so it is assignable to a consumer's own middleware type
// without this module importing theirs. See the package doc.
func Retry(p Policy) func(http.RoundTripper) http.RoundTripper {
	p = p.withDefaults()
	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return p.do(next, req)
		})
	}
}

// eligible reports whether this request may be retried at all, before any
// attempt is made. Task 3 adds the body-replay half of this check.
func (p Policy) eligible(req *http.Request) bool {
	method := req.Method
	if method == "" {
		method = http.MethodGet // net/http treats an empty method as GET
	}
	return slices.Contains(p.RetryMethods, method)
}

func (p Policy) do(next http.RoundTripper, req *http.Request) (*http.Response, error) {
	if !p.eligible(req) {
		return next.RoundTrip(req)
	}

	for attempt := 1; ; attempt++ {
		resp, err := next.RoundTrip(req)
		if attempt >= p.MaxAttempts || !p.RetryIf(resp, err) {
			return resp, err
		}

		delay := p.Backoff(attempt)

		// Check the deadline BEFORE discarding the response. If there is not
		// enough time left, the last result is still the caller's best answer
		// and its body is still readable.
		if !canWait(req.Context(), delay) {
			return resp, err
		}

		if err := wait(req.Context(), delay); err != nil {
			return nil, err
		}
	}
}

// canWait reports whether ctx has enough time left to sleep for d and still
// attempt something afterwards.
func canWait(ctx context.Context, d time.Duration) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline) > d
}

// wait sleeps for d, or returns ctx's error if it is cancelled first.
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
