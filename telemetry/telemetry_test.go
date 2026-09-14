package telemetry

import (
	"context"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TestZeroOptionsPropagatorIsW3C proves the default is a real propagator and
// not OTel's no-op global. A no-op default would make Server and Client
// compile, run, and propagate nothing -- the failure mode spec S4.1 exists to
// prevent.
func TestZeroOptionsPropagatorIsW3C(t *testing.T) {
	p := Options{}.propagator()

	in := http.Header{}
	in.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx := p.Extract(context.Background(), propagation.HeaderCarrier(in))

	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		t.Fatal("default propagator did not extract a valid span context")
	}
	if got := sc.TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q", got)
	}
	if !sc.IsRemote() {
		t.Error("extracted span context should be remote")
	}

	out := http.Header{}
	p.Inject(ctx, propagation.HeaderCarrier(out))
	if out.Get("traceparent") == "" {
		t.Error("default propagator injected no traceparent")
	}
}

// TestZeroOptionsPropagatorCarriesBaggage keeps Baggage in the composite. The
// baggage allowlist in LogExtractor is useless if the default propagator never
// extracts any.
func TestZeroOptionsPropagatorCarriesBaggage(t *testing.T) {
	p := Options{}.propagator()
	in := http.Header{}
	in.Set("baggage", "tenant_id=acme")
	ctx := p.Extract(context.Background(), propagation.HeaderCarrier(in))

	if got := baggage.FromContext(ctx).Member("tenant_id").Value(); got != "acme" {
		t.Fatalf("baggage tenant_id = %q, want %q", got, "acme")
	}
}

// TestEveryOptionLandsOnItsOwnDestination covers the struct-literal defaulting
// that go-mutesting cannot mutate (conventions S9, carried from R2 and R3).
// Each field gets a distinct observable value so a copy-paste that reads the
// wrong field fails.
func TestEveryOptionLandsOnItsOwnDestination(t *testing.T) {
	prop := headerPropagator{header: "x-marker-propagator"}
	tp := &recordingTracerProvider{}
	mp := &recordingMeterProvider{}

	o := Options{Propagator: prop, TracerProvider: tp, MeterProvider: mp}

	out := http.Header{}
	o.propagator().Inject(context.Background(), propagation.HeaderCarrier(out))
	if out.Get("x-marker-propagator") == "" {
		t.Error("Options.Propagator did not reach propagator()")
	}
	if o.tracer(); tp.name == "" {
		t.Error("Options.TracerProvider did not reach tracer()")
	}
	if o.meter(); mp.name == "" {
		t.Error("Options.MeterProvider did not reach meter()")
	}
}

// TestZeroOptionsTracerReachesGlobal pins that the nil-TracerProvider branch
// of tracer() resolves to the process-wide global TracerProvider rather than
// returning nil or panicking. It deliberately does NOT call
// otel.SetTracerProvider: no test may mutate process-wide OTel state, and the
// unconfigured global is already a usable (no-op) implementation, which is
// exactly what this resolver must hand back without choking on it.
func TestZeroOptionsTracerReachesGlobal(t *testing.T) {
	tr := Options{}.tracer()
	if tr == nil {
		t.Fatal("Options{}.tracer() returned nil")
	}

	_, span := tr.Start(context.Background(), "op")
	if span == nil {
		t.Fatal("global tracer's Start returned a nil span")
	}
}

// TestZeroOptionsMeterReachesGlobal pins the same contract for meter(): the
// nil-MeterProvider branch must resolve to the process-wide global
// MeterProvider, not panic or return something unusable. As above, no
// otel.SetMeterProvider call -- the unconfigured global default is what's
// under test.
func TestZeroOptionsMeterReachesGlobal(t *testing.T) {
	m := Options{}.meter()
	if m == nil {
		t.Fatal("Options{}.meter() returned nil")
	}

	h, err := m.Float64Histogram("x")
	if err != nil {
		t.Fatalf("global meter Float64Histogram error: %v", err)
	}
	if h == nil {
		t.Fatal("global meter returned a nil histogram")
	}
	h.Record(context.Background(), 1.0)
}

// TestDurationHistogramSurvivesAMeterThatErrors is the guard for spec S3.3:
// metric.Meter documents NO guarantee that the returned instrument is usable
// when the error is non-nil, so a nil instrument must not reach Record.
func TestDurationHistogramSurvivesAMeterThatErrors(t *testing.T) {
	h := durationHistogram(errMeter{}, "x", "y")
	if h == nil {
		t.Fatal("durationHistogram returned nil on the error path")
	}
	// The real assertion: this must not panic.
	h.Record(context.Background(), 1.0, metric.WithAttributes())
}
