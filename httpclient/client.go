package httpclient

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// Defaults applied to a non-positive Options field.
//
// Every value except the three noted below is http.DefaultTransport's own, read
// off go1.26 rather than chosen, so that the only deviations are deliberate.
// TestSharedDefaultsStillMatchTheStdlib re-derives the four shared values from
// http.DefaultTransport on every run, so a stdlib change fails a test here
// instead of rotting this sentence.
const (
	// Deviation 1. http.DefaultTransport uses 30s; we use 5s. A TCP connect
	// needing more than 5s means SYN retransmits — the dependency is effectively
	// down — so fail fast to free the goroutine and preserve the caller's
	// remaining deadline budget. A caller wanting the stdlib value sets the
	// Options field.
	defaultDialTimeout           = 5 * time.Second
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultIdleConnTimeout       = 90 * time.Second
	defaultExpectContinueTimeout = 1 * time.Second
	defaultKeepAlive             = 30 * time.Second
	defaultMaxIdleConns          = 100

	// Deviation 2. http.DefaultTransport leaves this zero, i.e. unbounded: a
	// server that accepts a connection and never sends headers hangs the
	// caller until its context expires, and forever if it has no deadline.
	defaultResponseHeaderTimeout = 10 * time.Second

	// Deviation 3. http.Transport leaves this zero, which means the package
	// default of 2 -- and http.DefaultTransport leaves it zero as well, so the
	// standard client has the same ceiling. Above that concurrency a service
	// opens a fresh connection per request, pays TCP and TLS setup each time,
	// and returns it to a pool that immediately discards it. It shows up as
	// latency nothing in the application explains.
	defaultMaxIdleConnsPerHost = 100
)

// Options configures New. Every non-positive value -- zero or negative --
// takes the documented default, for every field except Timeout: a negative
// duration reaching http.Transport or net.Dialer unclamped means NO deadline
// at all to those types, the opposite of what a caller setting a negative
// value could have intended, so it is treated the same as unset rather than
// passed through. Timeout is the one exception; see its own doc comment.
type Options struct {
	DialTimeout           time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	IdleConnTimeout       time.Duration
	ExpectContinueTimeout time.Duration
	MaxIdleConns          int
	MaxIdleConnsPerHost   int

	// Timeout bounds the ENTIRE exchange including reading the response body,
	// so it is deliberately not defaulted: a default here would break
	// streaming, SSE, long polling and large downloads at an arbitrary
	// boundary. Prefer a deadline on the request context. Set this only when
	// you know no response body is long-lived.
	//
	// Unlike every other field above, a non-positive value here is NOT
	// clamped: zero deliberately means "no blanket timeout" (spec 3.3), and a
	// negative value is passed through to http.Client.Timeout unchanged for
	// the same reason -- there is no default to fall back to.
	Timeout time.Duration

	// TLSClientConfig is handed to the transport as-is. It exists because
	// the alternative route to it -- asserting c.Transport to
	// *http.Transport after New returns -- is the path that breaks the
	// moment Middleware is set, so an mTLS or custom-CA caller would be
	// choosing between TLS and middleware. nil keeps the stdlib default.
	TLSClientConfig *tls.Config

	// Middleware wraps the transport. Chain folds several into one.
	Middleware Middleware
}

// New returns an http.Client configured per opts.
//
// It returns the stdlib type rather than a wrapper so the result drops into
// every API that expects one. The cost is that assigning c.Transport
// afterwards silently discards this configuration and any middleware with it.
//
// New never reads, copies from, or mutates http.DefaultTransport, and never
// returns http.DefaultClient. Those are process-wide singletons: tuning them
// re-tunes every other user in the process, and using them inherits whatever
// someone else did. This is the client-side form of the net/http/pprof rule in
// docs/conventions.md 7.
//
// The client owns no lifecycle, so there is nothing to close. To release idle
// connections at shutdown, register c.CloseIdleConnections with your
// lifecycle; examples/orders shows the wiring. That recipe holds with
// Middleware set: New forwards CloseIdleConnections through the wrapper, which
// http.Client would otherwise drop on the floor without an error.
//
// For mTLS or a custom CA, set Options.TLSClientConfig rather than reaching
// through c.Transport: the assertion to *http.Transport fails under
// Middleware, and failing that way costs you TLS verification silently.
func New(opts Options) *http.Client {
	tr := &http.Transport{
		DialContext: newDialer(opts).DialContext,

		// Setting DialContext above disables http.Transport's automatic HTTP/2,
		// which it only applies when Dial, DialContext and TLSClientConfig are
		// all nil. Without this line, configuring a dial timeout would silently
		// downgrade every caller to HTTP/1.1 -- no error, no log line.
		// http.DefaultTransport sets it for the same reason.
		ForceAttemptHTTP2: true,

		// A fresh Transport has Proxy: nil, so HTTP_PROXY, HTTPS_PROXY, and
		// NO_PROXY are ignored completely. In an egress-controlled network this
		// silently bypasses a security control or breaks every request without
		// error. This is the same silent loss as ForceAttemptHTTP2 above -- a
		// fresh transport loses DefaultTransport's behaviour, here its proxy
		// settings.
		Proxy: http.ProxyFromEnvironment,

		TLSHandshakeTimeout:   orDuration(opts.TLSHandshakeTimeout, defaultTLSHandshakeTimeout),
		ResponseHeaderTimeout: orDuration(opts.ResponseHeaderTimeout, defaultResponseHeaderTimeout),
		IdleConnTimeout:       orDuration(opts.IdleConnTimeout, defaultIdleConnTimeout),
		ExpectContinueTimeout: orDuration(opts.ExpectContinueTimeout, defaultExpectContinueTimeout),
		MaxIdleConns:          orInt(opts.MaxIdleConns, defaultMaxIdleConns),
		MaxIdleConnsPerHost:   orInt(opts.MaxIdleConnsPerHost, defaultMaxIdleConnsPerHost),
		TLSClientConfig:       opts.TLSClientConfig,
	}

	var rt http.RoundTripper = tr
	if opts.Middleware != nil {
		rt = forwardingRoundTripper{RoundTripper: opts.Middleware(rt), tr: tr}
	}

	return &http.Client{Transport: rt, Timeout: opts.Timeout}
}

// newDialer builds the dialer New installs as DialContext.
//
// Extracted so the two values inside it can be read back and asserted. Inlined
// in the Transport literal they were reachable from no test at all: the
// closure is only observable by dialing, and Options.DialTimeout could be
// unwired entirely -- or crossed with the keep-alive interval -- with the
// whole suite still green. Found at the R2 review.
func newDialer(opts Options) *net.Dialer {
	return &net.Dialer{
		Timeout:   orDuration(opts.DialTimeout, defaultDialTimeout),
		KeepAlive: defaultKeepAlive,
	}
}

// forwardingRoundTripper keeps CloseIdleConnections reachable through
// middleware.
//
// http.Client.CloseIdleConnections type-asserts c.Transport to
// interface{ CloseIdleConnections() } and does nothing at all when the
// assertion fails. RoundTripperFunc -- the canonical middleware wrapper, and
// the one this package exports -- has no such method, so without this type
// setting Options.Middleware would silently turn the shutdown recipe above
// into a no-op: no error, no log line, no compile failure, just idle sockets
// held for IdleConnTimeout past every rolling deploy. Same silent-loss shape
// as ForceAttemptHTTP2 and Proxy, one level up.
type forwardingRoundTripper struct {
	http.RoundTripper
	tr *http.Transport
}

// CloseIdleConnections forwards to the transport New built.
func (f forwardingRoundTripper) CloseIdleConnections() { f.tr.CloseIdleConnections() }

// orDuration returns d unless v is strictly positive. A non-positive v is
// clamped to the default rather than only a zero one: both http.Transport and
// net.Dialer treat a non-positive timeout as no deadline at all, so passing a
// negative value through would silently mean the opposite of what the caller
// asked for -- precisely the failure DialTimeout's Deviation 1 exists to
// prevent. Not used for Options.Timeout, which has no default; see its doc
// comment.
func orDuration(v, d time.Duration) time.Duration {
	if v <= 0 {
		return d
	}
	return v
}

// orInt returns d unless v is strictly positive, for the same reason
// orDuration clamps negatives: MaxIdleConns and MaxIdleConnsPerHost are
// counts, and net/http treats a non-positive value as "no limit" rather than
// "unset", so a negative Options value would silently disable pooling instead
// of taking its default.
func orInt(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}
