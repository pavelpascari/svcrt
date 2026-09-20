package telemetry_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/pavelpascari/svcrt/telemetry"
	"go.opentelemetry.io/otel/trace"
)

// inboundTraceparent is a W3C traceparent header, the thing an upstream sends.
// Using a fixed one is what makes these examples' output stable: with no OTel
// SDK installed the ids are not generated, they are the ones that arrived.
const inboundTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// exampleLogger writes JSON to stdout with no timestamp. A service would build
// this with svcrt/logging (or kit.NewLogger, which wires the extractor for
// you); telemetry does not import either, so this example uses stdlib slog.
func exampleLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

// ExampleServer instruments inbound requests. Note what happens with no OTel
// SDK installed, which is the default: the service originates no traces of its
// own, but it still reads the inbound traceparent and carries that trace id
// into every log line -- so the chain is not broken.
func ExampleServer() {
	log := exampleLogger()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(_ http.ResponseWriter, r *http.Request) {
		// In a service this is wired once, as an Extractor on the logger.
		log.LogAttrs(r.Context(), slog.LevelInfo, "fetching order",
			telemetry.LogExtractor()(r.Context())...)
	})

	// Server goes OUTSIDE httpserver.AccessLog, so the span is already in the
	// context when the access-log line is emitted.
	h := telemetry.Server(telemetry.Options{})(mux)

	req := httptest.NewRequest(http.MethodGet, "/orders/42", nil)
	req.Header.Set("traceparent", inboundTraceparent)
	h.ServeHTTP(httptest.NewRecorder(), req)

	// Output:
	// {"level":"INFO","msg":"fetching order","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}
}

// ExampleClient instruments outbound requests and injects trace context, so
// the next service downstream joins the same trace.
//
// Compose it INSIDE svcrt/resilience.Retry: that gives one span per attempt,
// and the attempts that failed stay visible.
func ExampleClient() {
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		fmt.Println("downstream received traceparent:", r.Header.Get("traceparent"))
	}))
	defer upstream.Close()

	// Wrap whatever transport the client already has. In a service that is
	// svcrt/httpclient.Options.Middleware, or kit.NewClient, which puts this
	// inside Retry for you.
	c := upstream.Client()
	c.Transport = telemetry.Client(telemetry.Options{})(c.Transport)

	// A real handler's request context already carries the span. Here it is
	// built by hand from the traceparent an upstream would have sent.
	req, err := http.NewRequestWithContext(ctxWithInboundSpan(), http.MethodGet, upstream.URL, nil)
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

	// Output:
	// downstream received traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
}

// ExampleLogExtractor is the seam that makes a log line findable from a trace
// and a trace findable from a log line. svcrt/logging takes one of these and
// never learns what a span is.
func ExampleLogExtractor() {
	log := exampleLogger()
	extract := telemetry.LogExtractor()

	ctx := ctxWithInboundSpan()
	log.LogAttrs(ctx, slog.LevelInfo, "in a request", extract(ctx)...)

	// Outside a request there is no span, and the extractor returns nil
	// rather than an all-zero trace id that would look real.
	log.LogAttrs(context.Background(), slog.LevelInfo, "startup",
		extract(context.Background())...)

	// Output:
	// {"level":"INFO","msg":"in a request","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}
	// {"level":"INFO","msg":"startup"}
}

// ctxWithInboundSpan builds the context a service has after extracting an
// inbound traceparent with no SDK installed: a valid, remote, non-recording
// span context and nothing else.
func ctxWithInboundSpan() context.Context {
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
