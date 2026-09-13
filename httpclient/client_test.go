package httpclient_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/httpclient"
)

// transportOf returns the *http.Transport New built. Valid only when Options
// carried no Middleware, since middleware replaces c.Transport with its wrapper.
func transportOf(t *testing.T, c *http.Client) *http.Transport {
	t.Helper()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("c.Transport is %T, want *http.Transport", c.Transport)
	}
	return tr
}

// http.Transport contains a sync.Mutex, so copying one by value trips vet's
// copylocks check -- and `go test` does not run copylocks, so it fails only in
// ci.sh's `go vet`. Snapshot the fields under test instead.
//
// ForceAttemptHTTP2, Proxy and DialContext are here deliberately: they are the
// three fields this module spent the milestone learning to care about, and the
// R2 review confirmed that without them New could downgrade the whole process
// to HTTP/1.1 and disable every proxy environment variable with the suite
// still green. Proxy and DialContext are funcs, which are not comparable --
// hence the uintptr: reflect.Value.Pointer() is a legal comparable stand-in
// for func identity, which is all this test needs, and it keeps the struct
// comparable so `before != after` compiles and copylocks stays happy.
type transportSnapshot struct {
	maxIdleConns          int
	maxIdleConnsPerHost   int
	idleConnTimeout       time.Duration
	tlsHandshakeTimeout   time.Duration
	responseHeaderTimeout time.Duration
	expectContinueTimeout time.Duration
	forceAttemptHTTP2     bool
	proxy                 uintptr
	dialContext           uintptr
}

// funcPointer returns a comparable identity for a func value, or 0 for nil.
// reflect.Value.Pointer panics on an invalid Value, which a nil interface
// yields, so the nil case is handled before reflect sees it.
func funcPointer(fn any) uintptr {
	if fn == nil {
		return 0
	}
	v := reflect.ValueOf(fn)
	if v.IsNil() {
		return 0
	}
	return v.Pointer()
}

func snapshotTransport(tr *http.Transport) transportSnapshot {
	return transportSnapshot{
		maxIdleConns:          tr.MaxIdleConns,
		maxIdleConnsPerHost:   tr.MaxIdleConnsPerHost,
		idleConnTimeout:       tr.IdleConnTimeout,
		tlsHandshakeTimeout:   tr.TLSHandshakeTimeout,
		responseHeaderTimeout: tr.ResponseHeaderTimeout,
		expectContinueTimeout: tr.ExpectContinueTimeout,
		forceAttemptHTTP2:     tr.ForceAttemptHTTP2,
		proxy:                 funcPointer(tr.Proxy),
		dialContext:           funcPointer(tr.DialContext),
	}
}

func TestZeroOptionsProduceTheDocumentedDefaults(t *testing.T) {
	t.Parallel()
	c := httpclient.New(httpclient.Options{})
	tr := transportOf(t, c)

	if got, want := tr.TLSHandshakeTimeout, 10*time.Second; got != want {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", got, want)
	}
	if got, want := tr.ResponseHeaderTimeout, 10*time.Second; got != want {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", got, want)
	}
	if got, want := tr.IdleConnTimeout, 90*time.Second; got != want {
		t.Errorf("IdleConnTimeout = %v, want %v", got, want)
	}
	if got, want := tr.ExpectContinueTimeout, 1*time.Second; got != want {
		t.Errorf("ExpectContinueTimeout = %v, want %v", got, want)
	}
	if got, want := tr.MaxIdleConns, 100; got != want {
		t.Errorf("MaxIdleConns = %d, want %d", got, want)
	}
	// The headline default. http.Transport leaves this zero, which means the
	// package default of 2, and http.DefaultTransport leaves it zero too. A
	// service calling one dependency above that concurrency opens a fresh
	// connection per request.
	if got, want := tr.MaxIdleConnsPerHost, 100; got != want {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", got, want)
	}
	// No blanket timeout by default: it would bound body reads and so break
	// streaming, SSE and long polls. See spec 3.3.
	if c.Timeout != 0 {
		t.Errorf("Client.Timeout = %v, want 0 (per-request deadlines belong on the context)", c.Timeout)
	}
}

// Every field gets a DISTINCT value. Identical values would pass even if two
// fields were wired to each other's destinations, and go-mutesting does not
// mutate struct-literal field assignments -- so this table is the only thing
// standing between a swapped wiring and a green 1.000 mutation score. R1 found
// four httpserver wirings that survived deletion undetected for exactly this
// reason.
func TestEveryOptionLandsOnItsOwnDestination(t *testing.T) {
	t.Parallel()
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	c := httpclient.New(httpclient.Options{
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   11 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second,
		IdleConnTimeout:       13 * time.Second,
		ExpectContinueTimeout: 14 * time.Second,
		MaxIdleConns:          15,
		MaxIdleConnsPerHost:   16,
		Timeout:               17 * time.Second,
	})
	tr := transportOf(t, c)

	for _, tc := range []struct {
		name string
		got  any
		want any
	}{
		{"TLSHandshakeTimeout", tr.TLSHandshakeTimeout, 11 * time.Second},
		{"ResponseHeaderTimeout", tr.ResponseHeaderTimeout, 12 * time.Second},
		{"IdleConnTimeout", tr.IdleConnTimeout, 13 * time.Second},
		{"ExpectContinueTimeout", tr.ExpectContinueTimeout, 14 * time.Second},
		{"MaxIdleConns", tr.MaxIdleConns, 15},
		{"MaxIdleConnsPerHost", tr.MaxIdleConnsPerHost, 16},
		{"Client.Timeout", c.Timeout, 17 * time.Second},
		// Pointer identity, which is as distinct as a value gets: nothing
		// else in Options could land here by accident.
		{"TLSClientConfig", tr.TLSClientConfig, tlsCfg},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestMiddlewareWrapsTheTransport(t *testing.T) {
	t.Parallel()
	middlewareCalled := false
	middleware := func(rt http.RoundTripper) http.RoundTripper {
		middlewareCalled = true
		return httpclient.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return rt.RoundTrip(req)
		})
	}

	c := httpclient.New(httpclient.Options{
		Middleware: middleware,
	})

	if !middlewareCalled {
		t.Error("middleware was not called")
	}
	// New wraps the middleware's return value in an unexported forwarding
	// type (see TestCloseIdleConnectionsSurvivesMiddleware), so the assertion
	// this test can make from outside the package is that c.Transport is no
	// longer the raw transport.
	if _, ok := c.Transport.(*http.Transport); ok {
		t.Error("c.Transport is the raw *http.Transport; middleware did not wrap it")
	}
}

func TestTransportHonoursTheProxyEnvironment(t *testing.T) {
	// Not t.Parallel: t.Setenv forbids it.
	t.Setenv("HTTP_PROXY", "http://egress.example:3128")

	tr := transportOf(t, httpclient.New(httpclient.Options{}))
	if tr.Proxy == nil {
		t.Fatal("Proxy is nil; HTTP_PROXY/HTTPS_PROXY/NO_PROXY are ignored and egress-proxied networks break")
	}

	req, _ := http.NewRequest("GET", "http://upstream.example/x", nil)
	u, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	if u == nil || u.Host != "egress.example:3128" {
		t.Errorf("Proxy resolved to %v, want http://egress.example:3128", u)
	}
}

// The module's central hygiene rule. http.DefaultTransport is a process-wide
// singleton: a library that tunes it re-tunes every other user in the process,
// including ones that never heard of this package. Stating the rule is not
// enough -- a regression here is invisible at every call site, so it is
// asserted.
func TestNewDoesNotTouchTheProcessGlobals(t *testing.T) {
	// Deliberately NOT t.Parallel: this test reads process-wide state, and a
	// parallel sibling constructing clients would make it meaningless.
	dt := http.DefaultTransport.(*http.Transport)
	before := snapshotTransport(dt)

	c := httpclient.New(httpclient.Options{
		MaxIdleConnsPerHost: 999,
		IdleConnTimeout:     42 * time.Second,
	})

	after := snapshotTransport(dt)
	if before != after {
		t.Error("New mutated http.DefaultTransport; it must build a fresh one")
	}

	if c.Transport == http.DefaultTransport {
		t.Error("New returned a client using http.DefaultTransport")
	}
	if c == http.DefaultClient {
		t.Error("New returned http.DefaultClient")
	}
}

// ForceAttemptHTTP2 is set because New supplies a DialContext, and
// http.Transport only negotiates HTTP/2 automatically when Dial, DialContext
// and TLSClientConfig are all nil. Asserting the field alone would prove the
// value was set, not that HTTP/2 actually negotiates with a custom dialer in
// play -- which is the thing that breaks. So this makes a real request.
func TestHTTP2SurvivesTheCustomDialer(t *testing.T) {
	t.Parallel()
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()

	c := httpclient.New(httpclient.Options{})
	tr := transportOf(t, c)
	// Trust the test server's certificate. Assigning TLSClientConfig is exactly
	// the condition that would disable automatic HTTP/2, which is what makes
	// this a real test of ForceAttemptHTTP2 rather than a formality.
	tr.TLSClientConfig = ts.Client().Transport.(*http.Transport).TLSClientConfig

	resp, err := c.Get(ts.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()

	if resp.Proto != "HTTP/2.0" {
		t.Errorf("resp.Proto = %q, want \"HTTP/2.0\" -- a custom DialContext disabled HTTP/2 silently", resp.Proto)
	}
}

func TestMiddlewareWrapsTheTransportNewBuilt(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	var seen []string
	mw := func(name string) httpclient.Middleware {
		return func(next http.RoundTripper) http.RoundTripper {
			return httpclient.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				seen = append(seen, name)
				return next.RoundTrip(r)
			})
		}
	}

	c := httpclient.New(httpclient.Options{
		Middleware: httpclient.Chain(mw("outer"), mw("inner")),
	})

	resp, err := c.Get(ts.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()

	// Both ran, outermost first, and the request still reached the server --
	// proving the chain wraps the real transport rather than replacing it.
	if len(seen) != 2 || seen[0] != "outer" || seen[1] != "inner" {
		t.Errorf("middleware order = %v, want [outer inner]", seen)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestNilMiddlewareLeavesTheTransportUnwrapped(t *testing.T) {
	t.Parallel()
	c := httpclient.New(httpclient.Options{Middleware: nil})
	if _, ok := c.Transport.(*http.Transport); !ok {
		t.Errorf("c.Transport is %T, want the *http.Transport New built", c.Transport)
	}
}

func TestContextCancellationAbortsAnInFlightRequest(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var served atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Store(true)
		<-release // hold the request open until the test releases it
	}))
	defer func() { close(release); ts.Close() }()

	c := httpclient.New(httpclient.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		resp, err := c.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()

	// Wait until the server has the request, so cancellation lands mid-flight
	// rather than before the dial.
	deadline := time.After(5 * time.Second) // same budget as the select below
	for !served.Load() {
		select {
		case <-deadline:
			t.Fatal("server never received the request within 5s; cancellation would not have landed mid-flight")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Do err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the context did not abort the request within 5s")
	}
}

// The third silent loss, found at the R2 review. http.Client.CloseIdleConnections
// type-asserts c.Transport to interface{ CloseIdleConnections() } and does
// nothing when the assertion fails, and RoundTripperFunc -- the wrapper this
// package exports for middleware authors -- has no such method. So before the
// forwarding wrapper, setting Options.Middleware silently disabled the exact
// shutdown recipe New's doc comment prescribes.
//
// Asserting the wrapper's type would prove the spelling. This makes a real
// request through middleware, reads the body to EOF so the connection is
// genuinely returned to the pool (an unread body is never pooled, which would
// make the test pass for the wrong reason), confirms the server still holds it
// open, and only then calls CloseIdleConnections -- so what is asserted is the
// connection actually going away.
func TestCloseIdleConnectionsSurvivesMiddleware(t *testing.T) {
	t.Parallel()
	var open atomic.Int64
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
		}
	}
	ts.Start()
	defer ts.Close()

	c := httpclient.New(httpclient.Options{
		Middleware: func(next http.RoundTripper) http.RoundTripper {
			return httpclient.RoundTripperFunc(next.RoundTrip)
		},
	})

	resp, err := c.Get(ts.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	resp.Body.Close()

	// Control: the connection must be pooled, or the assertion below would
	// hold no matter what CloseIdleConnections did.
	waitFor(t, func() bool { return open.Load() == 1 },
		"the request never produced a pooled connection, so this test could not observe one being closed")

	c.CloseIdleConnections()

	waitFor(t, func() bool { return open.Load() == 0 },
		"connection still open after CloseIdleConnections: middleware severed the shutdown path New documents")
}

// waitFor polls cond until it holds, failing with msg after 5s -- the same
// budget as every other bound in this file.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestZeroOptionsProduceTheDocumentedDefaults hard-codes these four values on
// purpose: they pin OUR contract, and a caller reading the package
// documentation should get what it says regardless of what the stdlib does
// next. This test pins the other half of the claim -- that those four are
// still http.DefaultTransport's own, which is what client.go's "read off
// go1.26" sentence asserts. Without it that sentence is a fact about a
// specific release with nothing to re-check it, and a stdlib change would rot
// the comment silently instead of failing here.
//
// The three deliberate deviations (DialTimeout, ResponseHeaderTimeout,
// MaxIdleConnsPerHost) are absent by design: they are where we disagree with
// the stdlib, so equality is exactly what must NOT be asserted.
func TestSharedDefaultsStillMatchTheStdlib(t *testing.T) {
	// Deliberately NOT t.Parallel: reads the process-wide
	// http.DefaultTransport, same reasoning as TestNewDoesNotTouchTheProcessGlobals.
	dt := http.DefaultTransport.(*http.Transport)
	tr := transportOf(t, httpclient.New(httpclient.Options{}))

	for _, tc := range []struct {
		name string
		got  any
		want any
	}{
		{"TLSHandshakeTimeout", tr.TLSHandshakeTimeout, dt.TLSHandshakeTimeout},
		{"IdleConnTimeout", tr.IdleConnTimeout, dt.IdleConnTimeout},
		{"ExpectContinueTimeout", tr.ExpectContinueTimeout, dt.ExpectContinueTimeout},
		{"MaxIdleConns", tr.MaxIdleConns, dt.MaxIdleConns},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, but http.DefaultTransport now uses %v; this value is documented as the stdlib's own, so either adopt the new one or record it as a fourth deliberate deviation",
				tc.name, tc.got, tc.want)
		}
	}
}
