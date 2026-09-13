package httpclient_test

import (
	"net/http"
	"net/http/httptest"
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
	c := httpclient.New(httpclient.Options{
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
	if _, ok := c.Transport.(httpclient.RoundTripperFunc); !ok {
		t.Errorf("c.Transport is %T, want httpclient.RoundTripperFunc", c.Transport)
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
	before := *dt // shallow copy of every exported field

	c := httpclient.New(httpclient.Options{
		MaxIdleConnsPerHost: 999,
		IdleConnTimeout:     42 * time.Second,
	})

	after := *dt
	if before.MaxIdleConnsPerHost != after.MaxIdleConnsPerHost ||
		before.IdleConnTimeout != after.IdleConnTimeout ||
		before.MaxIdleConns != after.MaxIdleConns ||
		before.TLSHandshakeTimeout != after.TLSHandshakeTimeout ||
		before.ResponseHeaderTimeout != after.ResponseHeaderTimeout ||
		before.ExpectContinueTimeout != after.ExpectContinueTimeout {
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
