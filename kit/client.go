package kit

import (
	"net/http"

	"github.com/pavelpascari/svcrt/httpclient"
	"github.com/pavelpascari/svcrt/resilience"
	"github.com/pavelpascari/svcrt/telemetry"
)

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ClientOptions configures NewClient. The zero value is a complete production
// stack: a tuned transport, three attempts with jittered exponential backoff,
// and W3C trace propagation against the global providers.
//
// The fields are the sibling modules' own option types rather than a mirrored
// surface. Mirroring would re-declare two dozen fields that drift the first
// time a sibling gains one, and every one of these zero values already means
// "defaults" -- so kit declares no defaults of its own and has no defaulting
// logic to get wrong.
type ClientOptions struct {
	// HTTP configures the transport. Its Middleware field, if set, is chained
	// innermost -- see NewClient.
	HTTP httpclient.Options

	// Retry configures the retry loop.
	Retry resilience.Policy

	// Telemetry configures the client span and duration histogram.
	Telemetry telemetry.Options
}

// NewClient returns an http.Client with retry, tracing and metrics composed in
// the one order that works:
//
//	Retry -> telemetry.Client -> o.HTTP.Middleware -> transport
//
// telemetry.Client sits INSIDE Retry so each attempt gets its own span. Outside,
// three attempts collapse into one span and the retries become invisible --
// the opposite of what you instrumented for, and nothing errors either way.
//
// o.HTTP.Middleware would otherwise be overwritten by the chain kit builds, so
// it is chained innermost rather than discarded. Innermost is also where it
// belongs: caller middleware is typically per-attempt work -- attaching a fresh
// auth token, signing a request -- and outside Retry it would run once and be
// replayed stale on every later attempt.
func NewClient(o ClientOptions) *http.Client {
	ms := []httpclient.Middleware{
		resilience.Retry(o.Retry),
		telemetry.Client(o.Telemetry),
	}
	if o.HTTP.Middleware != nil {
		ms = append(ms, o.HTTP.Middleware)
	}

	opts := o.HTTP
	opts.Middleware = httpclient.Chain(ms...)
	return httpclient.New(opts)
}
