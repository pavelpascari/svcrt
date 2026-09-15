package kit

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	tnoop "go.opentelemetry.io/otel/trace/noop"
)

// countingTracerProvider counts started spans. One span per retry attempt is
// the observable difference between the correct composition and the inverted
// one, so counting is the whole assertion.
type countingTracerProvider struct {
	embedded.TracerProvider
	mu sync.Mutex
	n  int
}

func (p *countingTracerProvider) Tracer(string, ...trace.TracerOption) trace.Tracer {
	return &countingTracer{p: p}
}

func (p *countingTracerProvider) spans() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

type countingTracer struct {
	embedded.Tracer
	p *countingTracerProvider
}

func (t *countingTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	t.p.mu.Lock()
	t.p.n++
	t.p.mu.Unlock()
	return tnoop.NewTracerProvider().Tracer("").Start(ctx, name, opts...)
}
