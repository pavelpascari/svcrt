// Package telemetry provides OpenTelemetry tracing and metrics for HTTP
// servers and clients, and the log extractor that correlates them.
//
// It depends on the OpenTelemetry API only, never the SDK. Choosing an
// exporter, sampler and resource is an application decision with an
// application's lifetime, and a library that makes it takes the choice away
// from every consumer.
//
// That split costs less than it appears to. With no SDK installed a service
// still reads an inbound traceparent, carries that trace id into its log
// lines, and passes it to its own upstreams: it originates no traces of its
// own but does not break the chain.
//
// This is the only svcrt module with dependencies, and the only one that
// needs them -- a tracing library that refused to depend on a tracing API
// would have to reimplement W3C tracecontext, which is not independence but a
// second, worse implementation of a standard.
package telemetry

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Well-known log attribute keys produced by LogExtractor.
//
// They live here rather than in svcrt/logging because this is the module that
// emits them: logging never learns what a span is. A module that does not
// produce an attribute should not be naming it.
const (
	KeyTraceID = "trace_id"
	KeySpanID  = "span_id"
)

// instrumentationName identifies this library as the source of spans and
// metrics. It names the instrumentation, not the service, so it is not
// configurable.
const instrumentationName = "github.com/pavelpascari/svcrt/telemetry"

// Options configures Server and Client. The zero value is the intended
// configuration for a service whose application wires an OTel SDK.
type Options struct {
	// Propagator carries trace context across process boundaries.
	// nil selects W3C TraceContext plus Baggage.
	Propagator propagation.TextMapPropagator

	// TracerProvider supplies the tracer. nil selects the global provider.
	TracerProvider trace.TracerProvider

	// MeterProvider supplies the meter. nil selects the global provider.
	MeterProvider metric.MeterProvider
}

// propagator returns the configured propagator, defaulting to W3C
// TraceContext plus Baggage.
//
// It deliberately does NOT fall back to otel.GetTextMapPropagator(). That
// global defaults to a no-op: extract yields an invalid span context and
// inject writes nothing. A service that wires this middleware and never calls
// otel.SetTextMapPropagator would get middleware that compiles, runs,
// allocates a span, and silently propagates nothing -- no error, no log line.
// A no-op TracerProvider means "not exporting traces", which is a legitimate
// state; a no-op propagator means "breaks the trace chain", which never is.
func (o Options) propagator() propagation.TextMapPropagator {
	if o.Propagator != nil {
		return o.Propagator
	}
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func (o Options) tracer() trace.Tracer {
	tp := o.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer(instrumentationName)
}

func (o Options) meter() metric.Meter {
	mp := o.MeterProvider
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	return mp.Meter(instrumentationName)
}

// durationHistogram builds a seconds-valued duration histogram, substituting
// an explicit no-op when the meter reports an error.
//
// metric.Meter's documentation makes no promise about the returned instrument
// when the error is non-nil: it does not state that a usable no-op comes back.
// Trusting an undocumented guarantee here would cost a nil dereference on
// every request, so the error path uses an instrument known to be safe.
func durationHistogram(m metric.Meter, name, desc string) metric.Float64Histogram {
	h, err := m.Float64Histogram(name,
		metric.WithUnit("s"),
		metric.WithDescription(desc),
	)
	if err != nil {
		h, _ = noop.Meter{}.Float64Histogram(name)
	}
	return h
}
