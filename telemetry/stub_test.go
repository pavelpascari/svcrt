package telemetry

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	membedded "go.opentelemetry.io/otel/metric/embedded"
	mnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tembedded "go.opentelemetry.io/otel/trace/embedded"
	tnoop "go.opentelemetry.io/otel/trace/noop"
)

// errMeter returns the (nil, error) pair metric.Meter's contract permits and
// naive code dereferences. Embedding noop.Meter supplies every other method.
type errMeter struct{ mnoop.Meter }

func (errMeter) Float64Histogram(string, ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return nil, errors.New("boom")
}

// measurement is one recorded histogram sample.
type measurement struct {
	name  string
	value float64
	attrs attribute.Set
}

// recordingMeterProvider captures measurements so tests assert on real output
// rather than on the fact that Record was called.
type recordingMeterProvider struct {
	membedded.MeterProvider
	mu   sync.Mutex
	name string
	got  []measurement
}

func (p *recordingMeterProvider) Meter(name string, _ ...metric.MeterOption) metric.Meter {
	p.mu.Lock()
	p.name = name
	p.mu.Unlock()
	return &recordingMeter{p: p}
}

func (p *recordingMeterProvider) records() []measurement {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]measurement(nil), p.got...)
}

type recordingMeter struct {
	mnoop.Meter
	p *recordingMeterProvider
}

func (m *recordingMeter) Float64Histogram(name string, _ ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return &recordingHistogram{p: m.p, name: name}, nil
}

type recordingHistogram struct {
	membedded.Float64Histogram
	p    *recordingMeterProvider
	name string
}

func (h *recordingHistogram) Record(_ context.Context, v float64, opts ...metric.RecordOption) {
	c := metric.NewRecordConfig(opts)
	h.p.mu.Lock()
	h.p.got = append(h.p.got, measurement{name: h.name, value: v, attrs: c.Attributes()})
	h.p.mu.Unlock()
}

// Enabled satisfies metric.Float64Histogram, which as of v1.46.0 requires it
// directly (not just via the embedded.Float64Histogram marker). The brief
// predates this addition to the API; a recording stub is always enabled.
func (h *recordingHistogram) Enabled(context.Context) bool { return true }

// recordingTracerProvider records the instrumentation name and hands out spans
// that capture their own name, kind, attributes and status.
type recordingTracerProvider struct {
	tembedded.TracerProvider
	mu    sync.Mutex
	name  string
	spans []*recordedSpan
}

func (p *recordingTracerProvider) Tracer(name string, _ ...trace.TracerOption) trace.Tracer {
	p.mu.Lock()
	p.name = name
	p.mu.Unlock()
	return &recordingTracer{p: p}
}

func (p *recordingTracerProvider) recorded() []*recordedSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*recordedSpan(nil), p.spans...)
}

type recordingTracer struct {
	tembedded.Tracer
	p *recordingTracerProvider
}

func (t *recordingTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	c := trace.NewSpanStartConfig(opts...)
	s := &recordedSpan{
		Span:  tnoop.Span{},
		name:  name,
		kind:  c.SpanKind(),
		attrs: c.Attributes(),
		// Carry the incoming span context forward so an extracted remote
		// trace id stays visible to LogExtractor, exactly as a real
		// non-recording span does.
		sc: trace.SpanContextFromContext(ctx),
	}
	t.p.mu.Lock()
	t.p.spans = append(t.p.spans, s)
	t.p.mu.Unlock()
	return trace.ContextWithSpan(ctx, s), s
}

type recordedSpan struct {
	trace.Span
	mu    sync.Mutex
	name  string
	kind  trace.SpanKind
	attrs []attribute.KeyValue
	code  codes.Code
	desc  string
	ended bool
	sc    trace.SpanContext
}

func (s *recordedSpan) SpanContext() trace.SpanContext { return s.sc }
func (s *recordedSpan) SetName(n string)               { s.mu.Lock(); s.name = n; s.mu.Unlock() }
func (s *recordedSpan) End(...trace.SpanEndOption)     { s.mu.Lock(); s.ended = true; s.mu.Unlock() }

func (s *recordedSpan) SetAttributes(kv ...attribute.KeyValue) {
	s.mu.Lock()
	s.attrs = append(s.attrs, kv...)
	s.mu.Unlock()
}

func (s *recordedSpan) SetStatus(c codes.Code, d string) {
	s.mu.Lock()
	s.code, s.desc = c, d
	s.mu.Unlock()
}

func (s *recordedSpan) attr(key string) (attribute.Value, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.attrs {
		if string(a.Key) == key {
			return a.Value, true
		}
	}
	return attribute.Value{}, false
}

// headerPropagator is a propagator that writes one distinctive header, so a
// test can prove Options.Propagator was used rather than the W3C default.
type headerPropagator struct{ header string }

func (p headerPropagator) Inject(_ context.Context, c propagation.TextMapCarrier) {
	c.Set(p.header, "1")
}
func (p headerPropagator) Extract(ctx context.Context, _ propagation.TextMapCarrier) context.Context {
	return ctx
}
func (p headerPropagator) Fields() []string { return []string{p.header} }
