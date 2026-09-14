package telemetry

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type fnRoundTripper func(*http.Request) (*http.Response, error)

func (f fnRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// okTransport returns a canned response and captures the request it was given,
// which is how the injection and no-mutation tests observe what went on the
// wire.
func okTransport(status int, seen **http.Request) http.RoundTripper {
	return fnRoundTripper(func(r *http.Request) (*http.Response, error) {
		*seen = r
		return &http.Response{
			StatusCode: status,
			Body:       http.NoBody,
			Request:    r,
		}, nil
	})
}

// TestClientInjectsTraceparent needs an ambient valid span context on the
// request already, not merely a traceparent header set by hand. recordingTracer
// (stub_test.go) mirrors otel/trace/noop's own Start -- a span started from a
// context with no existing valid span context stays invalid, exactly like the
// real no-op tracer this module falls back to without an SDK (see
// telemetry.go's package doc: "it originates no traces of its own"). So a
// request built with only a manually-set header, and no ambient span, would
// let this test pass on Clone alone preserving that header, without Inject
// ever running -- proving nothing about injection. ctxWithSpan (from
// extractor_test.go) gives the request the valid, already-extracted span
// context that a real inbound request would carry after Server's
// prop.Extract, which is the realistic precondition for Client's Inject to
// have anything to propagate.
func TestClientInjectsTraceparent(t *testing.T) {
	var seen *http.Request
	rt := Client(Options{
		TracerProvider: &recordingTracerProvider{},
		MeterProvider:  &recordingMeterProvider{},
	})(okTransport(http.StatusOK, &seen))

	req := httptest.NewRequest(http.MethodGet, "http://pricing.internal/quote", nil)
	req = req.WithContext(ctxWithSpan(t))
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}

	got := seen.Header.Get("traceparent")
	if got == "" {
		t.Fatal("no traceparent on the outbound request")
	}
	if !strings.Contains(got, "4bf92f3577b34da6a3ce929d0e0e4736") {
		t.Fatalf("traceparent = %q, want it to carry the ambient trace id", got)
	}
}

// TestClientDoesNotMutateTheCallersRequest is the landmine guard.
// http.RoundTripper's contract says RoundTrip must not modify the request, and
// injecting a header is a modification. With resilience.Retry in the chain the
// same request object is replayed, so a mutating transport corrupts attempt 2.
//
// As with TestClientInjectsTraceparent above, the request needs an ambient
// valid span context (ctxWithSpan) for Inject to actually write anything --
// otherwise this test would pass vacuously regardless of Clone vs WithContext,
// since neither request would ever gain a traceparent header to leak.
func TestClientDoesNotMutateTheCallersRequest(t *testing.T) {
	var seen *http.Request
	rt := Client(Options{
		TracerProvider: &recordingTracerProvider{},
		MeterProvider:  &recordingMeterProvider{},
	})(okTransport(http.StatusOK, &seen))

	req := httptest.NewRequest(http.MethodGet, "http://pricing.internal/quote", nil)
	req = req.WithContext(ctxWithSpan(t))
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}

	if got := req.Header.Get("traceparent"); got != "" {
		t.Fatalf("caller's request was mutated: traceparent = %q", got)
	}
	if seen == req {
		t.Fatal("the caller's own request object reached the next transport")
	}
	if seen.Header.Get("traceparent") == "" {
		t.Fatal("the clone carried no traceparent, so nothing was propagated")
	}
}

func TestClientRecordsSpanAndDuration(t *testing.T) {
	tp := &recordingTracerProvider{}
	mp := &recordingMeterProvider{}
	var seen *http.Request
	rt := Client(Options{TracerProvider: tp, MeterProvider: mp})(okTransport(http.StatusOK, &seen))

	req := httptest.NewRequest(http.MethodPost, "http://pricing.internal:8443/quote?k=v", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}

	s := tp.recorded()[0]
	if s.kind != trace.SpanKindClient {
		t.Errorf("span kind = %v, want client", s.kind)
	}
	if s.name != http.MethodPost {
		t.Errorf("span name = %q, want %q", s.name, http.MethodPost)
	}

	m := mp.records()[0]
	if m.name != "http.client.request.duration" {
		t.Errorf("instrument = %q", m.name)
	}
	// server.address is the host ALONE: no scheme, no port, no path, no query.
	// A full URL here is an unbounded label set.
	if v, ok := attrOf(m, "server.address"); !ok || v.AsString() != "pricing.internal" {
		t.Errorf("server.address = %v (present=%v), want %q", v, ok, "pricing.internal")
	}
	if v, ok := attrOf(m, "http.request.method"); !ok || v.AsString() != http.MethodPost {
		t.Errorf("http.request.method = %v (present=%v)", v, ok)
	}
	if v, ok := attrOf(m, "http.response.status_code"); !ok || v.AsInt64() != http.StatusOK {
		t.Errorf("http.response.status_code = %v (present=%v)", v, ok)
	}
}

// TestClientMarksFourAndFiveHundredsAsErrors: unlike the server side, a 4xx on
// an outbound call IS this client's problem -- it asked for something wrong.
func TestClientMarksFourAndFiveHundredsAsErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   codes.Code
	}{
		{http.StatusOK, codes.Unset},
		{http.StatusNotFound, codes.Error},
		{http.StatusInternalServerError, codes.Error},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			tp := &recordingTracerProvider{}
			var seen *http.Request
			rt := Client(Options{TracerProvider: tp, MeterProvider: &recordingMeterProvider{}})(
				okTransport(tc.status, &seen))

			if _, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, "http://x/y", nil)); err != nil {
				t.Fatal(err)
			}
			if got := tp.recorded()[0].code; got != tc.want {
				t.Errorf("status %d -> span code %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// TestClientRecordsOnTransportError: a call that never got a response is the
// one you most want in the latency histogram and the trace.
func TestClientRecordsOnTransportError(t *testing.T) {
	tp := &recordingTracerProvider{}
	mp := &recordingMeterProvider{}
	boom := errors.New("dial tcp: connection refused")

	rt := Client(Options{TracerProvider: tp, MeterProvider: mp})(
		fnRoundTripper(func(*http.Request) (*http.Response, error) { return nil, boom }))

	if _, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, "http://x/y", nil)); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want it returned unchanged", err)
	}

	s := tp.recorded()[0]
	if s.code != codes.Error {
		t.Errorf("span code = %v, want Error", s.code)
	}
	if !s.ended {
		t.Error("span not ended on the error path")
	}

	got := mp.records()
	if len(got) != 1 {
		t.Fatalf("expected a measurement on the error path, got %d", len(got))
	}
	if v, ok := attrOf(got[0], "http.response.status_code"); ok {
		t.Errorf("status_code = %v recorded for a call that got no response", v)
	}
}
