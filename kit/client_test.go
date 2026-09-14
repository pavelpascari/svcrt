package kit

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/httpclient"
	"github.com/pavelpascari/svcrt/resilience"
	"github.com/pavelpascari/svcrt/telemetry"
)

// flakyUpstream fails with 503 for the first n requests, then returns 200.
// 503 is in resilience's default retry set.
func flakyUpstream(t *testing.T, n int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= int32(n) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// fastRetry keeps the suite quick without changing what is under test. The
// zero Policy's jittered exponential would add ~300ms per case.
func fastRetry() resilience.Policy {
	return resilience.Policy{MaxAttempts: 3, Backoff: resilience.Constant(time.Millisecond)}
}

// TestNewClientRecordsOneSpanPerAttempt is the reason this module exists.
// telemetry.Client must sit INSIDE resilience.Retry: outside, three attempts
// collapse into one span and the retries become invisible -- which is the
// opposite of what you instrumented for, and nothing errors either way.
func TestNewClientRecordsOneSpanPerAttempt(t *testing.T) {
	up, hits := flakyUpstream(t, 2)
	tp := &countingTracerProvider{}

	c := NewClient(ClientOptions{
		Retry:     fastRetry(),
		Telemetry: telemetry.Options{TracerProvider: tp},
	})

	resp, err := c.Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := hits.Load(); got != 3 {
		t.Fatalf("upstream saw %d requests, want 3", got)
	}
	if got := tp.spans(); got != 3 {
		t.Fatalf("recorded %d spans for 3 attempts, want 3 -- telemetry.Client is not inside Retry", got)
	}
}

// TestCallerMiddlewareRunsOncePerAttempt pins the innermost position. Caller
// middleware is typically per-attempt work -- a fresh auth token, a signature.
// Outside Retry it would run once and be replayed stale on attempts 2 and 3.
func TestCallerMiddlewareRunsOncePerAttempt(t *testing.T) {
	up, _ := flakyUpstream(t, 2)
	var calls atomic.Int32

	c := NewClient(ClientOptions{
		Retry: fastRetry(),
		HTTP: httpclient.Options{
			Middleware: func(next http.RoundTripper) http.RoundTripper {
				return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					return next.RoundTrip(r)
				})
			},
		},
	})

	resp, err := c.Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := calls.Load(); got != 3 {
		t.Fatalf("caller middleware ran %d times across 3 attempts, want 3 -- it is not innermost", got)
	}
}

// TestCallerMiddlewareIsNotDiscarded guards the collision directly: kit builds
// the chain that HTTP.Middleware would otherwise occupy, and silently dropping
// it is exactly the failure this module exists to prevent.
func TestCallerMiddlewareIsNotDiscarded(t *testing.T) {
	up, _ := flakyUpstream(t, 0)
	var ran atomic.Bool

	c := NewClient(ClientOptions{
		HTTP: httpclient.Options{
			Middleware: func(next http.RoundTripper) http.RoundTripper {
				return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
					ran.Store(true)
					return next.RoundTrip(r)
				})
			},
		},
	})

	resp, err := c.Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !ran.Load() {
		t.Fatal("caller middleware never ran; kit discarded HTTP.Middleware")
	}
}

// TestZeroClientOptionsIsAWorkingStack: the zero value must be a complete
// production stack, with no SDK installed.
func TestZeroClientOptionsIsAWorkingStack(t *testing.T) {
	up, hits := flakyUpstream(t, 0)

	resp, err := NewClient(ClientOptions{}).Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream saw %d requests, want 1", got)
	}
}

// TestEveryClientOptionLandsOnItsOwnDestination covers the struct-literal
// defaulting that go-mutesting does not mutate (carried from R2 and R3). Each
// field gets a distinct observable effect.
func TestEveryClientOptionLandsOnItsOwnDestination(t *testing.T) {
	up, hits := flakyUpstream(t, 2)
	tp := &countingTracerProvider{}
	var mwRan atomic.Bool

	c := NewClient(ClientOptions{
		// Retry: reaches the retry loop -- proven by 3 upstream hits.
		Retry: fastRetry(),
		// Telemetry: reaches telemetry.Client -- proven by recorded spans.
		Telemetry: telemetry.Options{TracerProvider: tp},
		// HTTP: reaches httpclient.New -- proven by the middleware running.
		HTTP: httpclient.Options{
			Middleware: func(next http.RoundTripper) http.RoundTripper {
				return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
					mwRan.Store(true)
					return next.RoundTrip(r)
				})
			},
		},
	})

	resp, err := c.Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := hits.Load(); got != 3 {
		t.Errorf("ClientOptions.Retry did not reach the retry loop: %d attempts", got)
	}
	if got := tp.spans(); got == 0 {
		t.Error("ClientOptions.Telemetry did not reach telemetry.Client")
	}
	if !mwRan.Load() {
		t.Error("ClientOptions.HTTP did not reach httpclient.New")
	}
}

// TestClientOptionsHTTPTimeoutReachesHttpclientNew guards the HTTP field
// against a narrower rebuild that forwards Middleware but drops the rest of
// o.HTTP -- e.g. `opts := httpclient.Options{}` instead of `opts := o.HTTP`.
// TestEveryClientOptionLandsOnItsOwnDestination does not catch that: it only
// observes Middleware running, which survives such a rebuild unchanged.
// Timeout is a second, independent field on the same struct, so requiring it
// to be enforced proves the whole of o.HTTP reaches httpclient.New, not just
// the one field the chain is built from.
func TestClientOptionsHTTPTimeoutReachesHttpclientNew(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(up.Close)

	c := NewClient(ClientOptions{
		HTTP: httpclient.Options{Timeout: 5 * time.Millisecond},
	})

	if _, err := c.Get(up.URL); err == nil {
		t.Fatal("expected a timeout error, got nil -- ClientOptions.HTTP.Timeout did not reach httpclient.New")
	}
}
