package telemetry

import (
	"context"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// ctxWithSpan builds a context carrying a valid, non-recording span context --
// exactly what a service with no SDK gets after extracting an inbound
// traceparent. No SDK is involved.
func ctxWithSpan(t *testing.T) context.Context {
	t.Helper()
	tid, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithSpanContext(context.Background(), sc)
}

func attrValue(attrs []slog.Attr, key string) (slog.Value, bool) {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return slog.Value{}, false
}

func TestLogExtractorEmitsTraceAndSpanID(t *testing.T) {
	attrs := LogExtractor()(ctxWithSpan(t))

	v, ok := attrValue(attrs, KeyTraceID)
	if !ok {
		t.Fatalf("no %s attribute in %v", KeyTraceID, attrs)
	}
	if got := v.String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("%s = %q", KeyTraceID, got)
	}

	v, ok = attrValue(attrs, KeySpanID)
	if !ok {
		t.Fatalf("no %s attribute in %v", KeySpanID, attrs)
	}
	if got := v.String(); got != "00f067aa0ba902b7" {
		t.Errorf("%s = %q", KeySpanID, got)
	}
}

// TestLogExtractorReturnsNilWithoutASpan is the load-bearing one. An extractor
// that skipped the IsValid check would stamp
// "trace_id":"00000000000000000000000000000000" on every startup, shutdown and
// background-job line -- a well-formed field carrying a lie, which also
// defeats grep-by-trace-id for the lines that do have one.
func TestLogExtractorReturnsNilWithoutASpan(t *testing.T) {
	if attrs := LogExtractor()(context.Background()); attrs != nil {
		t.Fatalf("expected nil for a context with no span, got %v", attrs)
	}
}

// TestLogExtractorRejectsAnInvalidSpanContext covers the boundary directly: a
// span context that exists but carries zero ids must be treated as absent.
func TestLogExtractorRejectsAnInvalidSpanContext(t *testing.T) {
	ctx := trace.ContextWithSpanContext(context.Background(), trace.SpanContext{})
	if attrs := LogExtractor()(ctx); attrs != nil {
		t.Fatalf("expected nil for an invalid span context, got %v", attrs)
	}
}

// TestLogExtractorDoesNotPanic enforces svcrt/logging's Extractor contract,
// which states the no-panic requirement in unusual detail: nothing recovers,
// so a panicking extractor propagates into the request being logged.
func TestLogExtractorDoesNotPanic(t *testing.T) {
	cases := map[string]context.Context{
		"background":       context.Background(),
		"todo":             context.TODO(),
		"no span":          context.WithValue(context.Background(), struct{}{}, "x"),
		"invalid span ctx": trace.ContextWithSpanContext(context.Background(), trace.SpanContext{}),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("LogExtractor panicked: %v", r)
				}
			}()
			LogExtractor("tenant_id", "absent")(ctx)
		})
	}
}
