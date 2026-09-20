package kit_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"time"

	"github.com/pavelpascari/svcrt/kit"
	"github.com/pavelpascari/svcrt/logging"
	"github.com/pavelpascari/svcrt/resilience"
	"go.opentelemetry.io/otel/trace"
)

// dropTime keeps these examples' output stable. A real service keeps the
// timestamp; it is the one thing an example has to remove.
func dropTime(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

// fastRetry keeps the examples quick without changing what they show. The zero
// Policy's jittered exponential would add roughly 300ms per call.
func fastRetry() resilience.Policy {
	return resilience.Policy{MaxAttempts: 3, Backoff: resilience.Constant(time.Millisecond)}
}

// inboundSpan is the context a service has while handling a request whose
// upstream sent a traceparent. Fixing the ids is what makes the output stable:
// with no OTel SDK installed they are not generated, they are what arrived.
func inboundSpan() context.Context {
	tid, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		panic(err)
	}
	sid, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		panic(err)
	}
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	}))
}

// ExampleNewClient builds the client stack in the one order that works:
// Breaker outside Retry, telemetry.Client inside it, caller middleware
// innermost. Every field is a sibling module's own option type, so the zero
// value is already a complete production stack.
func ExampleNewClient() {
	var attempts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 503 is in resilience's default retry set, so the first two
		// attempts are retried and the third succeeds.
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	c := kit.NewClient(kit.ClientOptions{Retry: fastRetry()})

	resp, err := c.Get(upstream.URL + "/orders")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	resp.Body.Close()

	fmt.Println("status:", resp.StatusCode)
	fmt.Println("attempts:", attempts.Load())

	// Output:
	// status: 200
	// attempts: 3
}

// ExampleNewLogger wires the extractor that makes a log line reachable from a
// trace and a trace reachable from a log line.
//
// This constructor exists because omitting the extractor is undetectable:
// svcrt/logging knows nothing about spans, so a logger built the ordinary way
// produces correct output that simply cannot be pivoted to the trace.
func ExampleNewLogger() {
	log := kit.NewLogger(os.Stdout, kit.LoggerOptions{
		Logging: logging.Options{ReplaceAttr: dropTime},
	})

	log.InfoContext(inboundSpan(), "order placed", "order_id", "ord_1")

	// Outside a request there is no span, and nothing is stamped -- rather
	// than an all-zero trace id that would look real.
	log.InfoContext(context.Background(), "startup complete")

	// Output:
	// {"level":"INFO","msg":"order placed","order_id":"ord_1","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}
	// {"level":"INFO","msg":"startup complete"}
}

// Example puts the two together, which is the whole point of this package: a
// call that is retried, traced and logged, with the same trace id on the log
// line and on the wire to the next service.
func Example() {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("downstream received traceparent:", r.Header.Get("traceparent"))
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	log := kit.NewLogger(os.Stdout, kit.LoggerOptions{
		Logging: logging.Options{ReplaceAttr: dropTime},
	})
	c := kit.NewClient(kit.ClientOptions{Retry: fastRetry()})

	ctx := inboundSpan()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL+"/orders", nil)
	if err != nil {
		fmt.Println("request:", err)
		return
	}
	resp, err := c.Do(req)
	if err != nil {
		fmt.Println("do:", err)
		return
	}
	resp.Body.Close()

	log.InfoContext(ctx, "fetched orders", "status", resp.StatusCode)

	// Output:
	// downstream received traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
	// {"level":"INFO","msg":"fetched orders","status":200,"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}
}
