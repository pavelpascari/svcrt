package httpclient

import (
	"net"
	"net/http"
	"time"
)

// Defaults applied to a zero-valued Options field.
//
// Every value except the three noted below is http.DefaultTransport's own, read
// off go1.25 rather than chosen, so that the only deviations are deliberate.
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

// Options configures New. Every zero-valued field takes the documented default.
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
	Timeout time.Duration

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
// lifecycle; examples/orders shows the wiring.
func New(opts Options) *http.Client {
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   orDuration(opts.DialTimeout, defaultDialTimeout),
			KeepAlive: defaultKeepAlive,
		}).DialContext,

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
	}

	var rt http.RoundTripper = tr
	if opts.Middleware != nil {
		rt = opts.Middleware(rt)
	}

	return &http.Client{Transport: rt, Timeout: opts.Timeout}
}

func orDuration(v, d time.Duration) time.Duration {
	if v == 0 {
		return d
	}
	return v
}

func orInt(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}
