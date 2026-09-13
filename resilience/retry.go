package resilience

import (
	"context"
	"io"
	"net/http"
	"slices"
	"time"
)

// maxDrain caps how much of a discarded response body is read before giving up
// on reusing its connection.
//
// Draining to EOF is what returns the connection to the pool. Draining without
// a cap means a hostile or broken upstream can hold the client reading an
// unbounded error page on every retry, which is a denial of service against
// the caller. 64KB covers a real error page; past that, closing early and
// paying for a new connection is the cheaper failure.
const maxDrain = 64 << 10

// maxRetryAfter caps how long a Retry-After header may park a request.
//
// Same reasoning as maxDrain, and the same threat model: a server saying when
// to come back is better information than a local curve, but an unbounded one
// is a denial of service against the caller, and it costs the attacker one
// header. canWait is no defence -- it only engages when the request carries a
// deadline, and httpclient.New deliberately does not default Options.Timeout,
// so `Retry-After: 86400` on a background context holds the caller's
// goroutine, its connection and any lock it owns for a day.
//
// 30s is chosen to sit above what real upstreams ask for -- 429 and 503
// Retry-After values in practice are single-digit to low-tens of seconds, and
// those are honoured to the second -- and below any plausible
// request-handling budget, so the cap only ever engages on a value no caller
// wanted to wait for anyway.
//
// A header over the cap falls back to the policy backoff rather than clamping
// to 30s. A server asking for an hour is saying "do not come back soon", and
// the jittered exponential (capped at 2s by default, and spread across
// clients) respects that better than a hard 30s wall of synchronised retries
// would. Clamping would also quietly convert every hostile header into the
// longest wait this code permits, which is the attacker's goal minus a
// constant factor.
//
// This is deliberately not a Policy field. Every field is another way to get
// the default wrong, the honest way to say "this call may take longer" is a
// context deadline -- which canWait already respects exactly -- and maxDrain
// sets the precedent: a bound that exists to survive a hostile peer is not a
// tuning knob.
const maxRetryAfter = 30 * time.Second

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

// eligible reports whether this request may be retried at all.
//
// Two gates, both checked before the first attempt:
//
// Replayability -- http.Request.Body is consumed by the first send, so a retry
// needs GetBody to rebuild it. http.NewRequest populates GetBody only for body
// types it recognises (bytes.Reader, bytes.Buffer, strings.Reader); for an
// opaque io.Reader it is nil. Retrying such a request would send a well-formed
// request with an EMPTY body on every attempt after the first, with no error
// raised anywhere. Refusing to retry is the honest outcome.
//
// Idempotency -- a lost response does not mean the request did not happen.
func (p Policy) eligible(req *http.Request) bool {
	if req.Body != nil && req.GetBody == nil {
		return false
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet // net/http treats an empty method as GET
	}
	return slices.Contains(p.RetryMethods, method)
}

// attemptRequest returns the request to send for this attempt. The first uses
// the original; later ones rebuild the body from GetBody, because the previous
// attempt consumed it.
func attemptRequest(req *http.Request, attempt int) (*http.Request, error) {
	if attempt == 1 || req.GetBody == nil {
		return req, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Body = body
	return clone, nil
}

// drain reads a discarded response far enough to let its connection be reused,
// then closes it. See maxDrain.
func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
	_ = resp.Body.Close()
}

// retryable asks the policy whether this result is worth another attempt.
//
// The defer is for the panicking case only. RetryIf is caller-supplied code
// running on the request path, so a panic in it is caller error and must
// propagate unchanged -- but the response in hand belongs to this loop, and
// unwinding past it would leave a body nobody can reach and a connection
// nobody can reuse. drain is the loop's own verb for discarding a response
// and tolerates a nil one.
func (p Policy) retryable(resp *http.Response, err error) bool {
	decided := false
	defer func() {
		if !decided {
			drain(resp)
		}
	}()
	retry := p.RetryIf(resp, err)
	decided = true
	return retry
}

func (p Policy) do(next http.RoundTripper, req *http.Request) (*http.Response, error) {
	if !p.eligible(req) {
		return next.RoundTrip(req)
	}

	for attempt := 1; ; attempt++ {
		attemptReq, buildErr := attemptRequest(req, attempt)
		if buildErr != nil {
			return nil, buildErr
		}

		resp, err := next.RoundTrip(attemptReq)
		if attempt >= p.MaxAttempts || !p.retryable(resp, err) {
			return resp, err
		}

		delay := p.Backoff(attempt)
		if d, ok := retryAfter(resp, time.Now()); ok && d <= maxRetryAfter {
			delay = d
		}

		// Check the deadline BEFORE discarding the response. If there is not
		// enough time left, the last result is still the caller's best answer
		// and its body is still readable.
		if !canWait(req.Context(), delay, time.Now()) {
			return resp, err
		}

		drain(resp)

		if err := wait(req.Context(), delay); err != nil {
			return nil, err
		}
	}
}

// canWait reports whether ctx has enough time left to sleep for d and still
// attempt something afterwards.
//
// now is a parameter rather than a time.Now() call, like retryAfter's,
// because the exact boundary -- remaining time equal to d, which must NOT be
// enough -- can only be hit reliably by controlling both sides of the
// subtraction. Two independent time.Now() calls (one to build a deadline in
// a test, one inside this function) never land on the same nanosecond, so
// that boundary is untestable without this seam.
func canWait(ctx context.Context, d time.Duration, now time.Time) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return deadline.Sub(now) > d
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
