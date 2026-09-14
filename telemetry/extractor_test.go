package telemetry

import (
	"context"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/baggage"
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

// ctxWithBaggage attaches two members so allowlist tests can prove the
// non-allowlisted one is dropped rather than merely that the allowlisted one
// is present.
func ctxWithBaggage(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	tenant, err := baggage.NewMember("tenant_id", "acme")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := baggage.NewMember("internal_debug", "leak-me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := baggage.New(tenant, secret)
	if err != nil {
		t.Fatal(err)
	}
	return baggage.ContextWithBaggage(ctx, b)
}

func TestLogExtractorEmitsAllowlistedBaggage(t *testing.T) {
	ctx := ctxWithBaggage(t, ctxWithSpan(t))
	attrs := LogExtractor("tenant_id")(ctx)

	v, ok := attrValue(attrs, "tenant_id")
	if !ok {
		t.Fatalf("tenant_id missing from %v", attrs)
	}
	if got := v.String(); got != "acme" {
		t.Errorf("tenant_id = %q", got)
	}
}

// TestLogExtractorDropsNonAllowlistedBaggage is the security-relevant half.
// Baggage is attacker-controllable on any request that reaches this service.
func TestLogExtractorDropsNonAllowlistedBaggage(t *testing.T) {
	ctx := ctxWithBaggage(t, ctxWithSpan(t))
	attrs := LogExtractor("tenant_id")(ctx)

	if _, ok := attrValue(attrs, "internal_debug"); ok {
		t.Fatalf("non-allowlisted baggage member leaked into %v", attrs)
	}
}

// TestLogExtractorWithNoAllowlistEmitsNoBaggage proves the safe default is the
// one you get by writing nothing.
func TestLogExtractorWithNoAllowlistEmitsNoBaggage(t *testing.T) {
	ctx := ctxWithBaggage(t, ctxWithSpan(t))
	attrs := LogExtractor()(ctx)

	if _, ok := attrValue(attrs, "tenant_id"); ok {
		t.Fatalf("baggage emitted with an empty allowlist: %v", attrs)
	}
	if len(attrs) != 2 {
		t.Errorf("expected exactly trace_id and span_id, got %v", attrs)
	}
}

// TestLogExtractorSkipsAbsentAllowlistedKey: an allowlisted key that is not in
// the baggage must be omitted, not emitted empty.
func TestLogExtractorSkipsAbsentAllowlistedKey(t *testing.T) {
	ctx := ctxWithBaggage(t, ctxWithSpan(t))
	attrs := LogExtractor("tenant_id", "never_set")(ctx)

	if _, ok := attrValue(attrs, "never_set"); ok {
		t.Fatalf("absent baggage key emitted as an attribute: %v", attrs)
	}
}

// TestLogExtractorEmitsBaggageWithoutASpan: baggage and span context are
// independent. A request with baggage but no traceparent still gets its
// allowlisted members.
func TestLogExtractorEmitsBaggageWithoutASpan(t *testing.T) {
	ctx := ctxWithBaggage(t, context.Background())
	attrs := LogExtractor("tenant_id")(ctx)

	if _, ok := attrValue(attrs, KeyTraceID); ok {
		t.Error("trace_id emitted without a valid span context")
	}
	if _, ok := attrValue(attrs, "tenant_id"); !ok {
		t.Fatalf("baggage dropped when no span was present: %v", attrs)
	}
}

// TestLogExtractorCopiesTheAllowlist: mutating the caller's slice afterwards
// must not change which members are trusted.
func TestLogExtractorCopiesTheAllowlist(t *testing.T) {
	keys := []string{"tenant_id"}
	ex := LogExtractor(keys...)
	keys[0] = "internal_debug"

	attrs := ex(ctxWithBaggage(t, ctxWithSpan(t)))
	if _, ok := attrValue(attrs, "internal_debug"); ok {
		t.Fatal("mutating the caller's slice changed the allowlist")
	}
	if _, ok := attrValue(attrs, "tenant_id"); !ok {
		t.Fatal("allowlist did not survive mutation of the caller's slice")
	}
}
