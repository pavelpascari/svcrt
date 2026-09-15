package kit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"

	"github.com/pavelpascari/svcrt/logging"
)

const testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

// ctxWithSpan builds a context carrying a valid, non-recording span context --
// what a service with no SDK gets after extracting an inbound traceparent.
func ctxWithSpan(t *testing.T) context.Context {
	t.Helper()
	tid, err := trace.TraceIDFromHex(testTraceID)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(
		trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true},
	))
}

// TestNewLoggerCorrelatesInsideASpan is the second reason this module exists.
// Nothing about building a logger suggests it has anything to do with tracing
// -- svcrt/logging deliberately knows nothing about spans -- so omitting the
// extractor produces correct spans, correct metrics, and uncorrelated logs,
// with nothing to notice.
func TestNewLoggerCorrelatesInsideASpan(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(&buf, LoggerOptions{}).InfoContext(ctxWithSpan(t), "hello")

	if !strings.Contains(buf.String(), testTraceID) {
		t.Fatalf("log line carries no trace id -- LogExtractor is not wired:\n%s", buf.String())
	}
}

// TestNewLoggerEmitsNoTraceIDOutsideASpan: a startup or shutdown line must not
// carry an all-zero trace id, which would be a well-formed field carrying a lie.
func TestNewLoggerEmitsNoTraceIDOutsideASpan(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(&buf, LoggerOptions{}).Info("starting")

	if strings.Contains(buf.String(), "trace_id") {
		t.Fatalf("trace_id emitted outside a span:\n%s", buf.String())
	}
}

func TestNewLoggerForwardsAllowlistedBaggage(t *testing.T) {
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
	ctx := baggage.ContextWithBaggage(ctxWithSpan(t), b)

	var buf bytes.Buffer
	NewLogger(&buf, LoggerOptions{Baggage: []string{"tenant_id"}}).InfoContext(ctx, "hello")

	out := buf.String()
	if !strings.Contains(out, "acme") {
		t.Errorf("allowlisted baggage missing:\n%s", out)
	}
	if strings.Contains(out, "leak-me") {
		t.Errorf("non-allowlisted baggage leaked:\n%s", out)
	}
}

// TestEveryLoggerOptionLandsOnItsOwnDestination: Logging must reach logging.New
// and Baggage must reach LogExtractor. Level is the observable for the first.
func TestEveryLoggerOptionLandsOnItsOwnDestination(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, LoggerOptions{
		Logging: logging.Options{Level: slog.LevelError},
		Baggage: []string{"tenant_id"},
	})

	log.InfoContext(ctxWithSpan(t), "should be filtered")
	if buf.Len() != 0 {
		t.Errorf("LoggerOptions.Logging did not reach logging.New; Info was emitted at Error level:\n%s", buf.String())
	}
}
