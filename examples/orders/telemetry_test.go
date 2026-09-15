package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pavelpascari/svcrt/httpclient"
	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/logging"
	"github.com/pavelpascari/svcrt/resilience"
	"github.com/pavelpascari/svcrt/telemetry"
)

// Compile-time proof that telemetry's constructors are assignable to the named
// middleware types of modules telemetry does not import. Go permits assignment
// between a named and an unnamed type with the same underlying type, but NOT
// between two different named types -- so this breaks the moment telemetry
// declares a named type for any of these returns. See docs/conventions.md S2.
var (
	_ logging.Extractor     = telemetry.LogExtractor()
	_ httpserver.Middleware = telemetry.Server(telemetry.Options{})
	_ httpclient.Middleware = telemetry.Client(telemetry.Options{})
)

// TestInboundTraceReachesLogsAndUpstream is the end-to-end claim of R5: one
// trace id, arriving in a header, must appear in this service's log output and
// on the request it makes to its own upstream.
//
// It runs with no OTel SDK installed. A service with only the API still
// continues an inbound trace -- it originates none of its own, but it does not
// break the chain.
func TestInboundTraceReachesLogsAndUpstream(t *testing.T) {
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	// The upstream this service calls; it records what arrived.
	var upstreamTraceparent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamTraceparent = r.Header.Get("traceparent")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"amount_cents":1250,"currency":"EUR"}`)
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	log := logging.New(&logs, logging.Options{Level: slog.LevelInfo}, telemetry.LogExtractor())

	client := httpclient.New(httpclient.Options{
		Middleware: httpclient.Chain(
			resilience.Retry(resilience.Policy{MaxAttempts: 2}),
			// Inside Retry: one span per attempt.
			telemetry.Client(telemetry.Options{}),
		),
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("upstream call: %v", err)
			return
		}
		resp.Body.Close()
		w.WriteHeader(http.StatusOK)
	})

	// telemetry.Server OUTSIDE AccessLog, so the span is in the context by the
	// time the access-log line is emitted.
	h := httpserver.Chain(
		telemetry.Server(telemetry.Options{}),
		httpserver.AccessLog(log),
	)(mux)

	req := httptest.NewRequest(http.MethodGet, "/orders/123", nil)
	req.Header.Set("traceparent", traceparent)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(logs.String(), traceID) {
		t.Errorf("access log carries no trace id:\n%s", logs.String())
	}
	if upstreamTraceparent == "" {
		t.Fatal("upstream received no traceparent")
	}
	if !strings.Contains(upstreamTraceparent, traceID) {
		t.Errorf("upstream traceparent %q does not carry the inbound trace id %s",
			upstreamTraceparent, traceID)
	}
}

// TestAccessLogLosesTheTraceIDWhenTelemetryIsInnermost proves the composition
// order in spec S5 is load-bearing rather than stylistic. Inverted, nothing
// errors -- the line is simply uncorrelated, which is why it needs a test.
func TestAccessLogLosesTheTraceIDWhenTelemetryIsInnermost(t *testing.T) {
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	var logs bytes.Buffer
	log := logging.New(&logs, logging.Options{Level: slog.LevelInfo}, telemetry.LogExtractor())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {})

	// Deliberately the WRONG order.
	h := httpserver.Chain(
		httpserver.AccessLog(log),
		telemetry.Server(telemetry.Options{}),
	)(mux)

	req := httptest.NewRequest(http.MethodGet, "/orders/123", nil)
	req.Header.Set("traceparent", traceparent)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(logs.String(), traceID) {
		t.Fatal("access log carried a trace id with telemetry composed innermost; " +
			"if this now passes, the ordering guidance in the spec is obsolete and " +
			"both it and this test must be updated together")
	}
}

// TestAcceptanceProductionHandlerCorrelatesItsAccessLog closes a gap the other
// tests in this file leave open: they build their own middleware chain, so
// none of them fails if telemetry.Server is deleted from newServer -- the one
// place production actually wires it.
//
// Removing it used to change nothing here. newTestServer builds its logger
// with plain logging.New and no extractor, so no existing test can observe
// trace context at all, and the absence of a span looks identical to a logger
// that was never going to report one.
//
// This drives the PRODUCTION newServer with a logger that can see spans, so
// deleting telemetry.Server from that chain fails here.
func TestAcceptanceProductionHandlerCorrelatesItsAccessLog(t *testing.T) {
	t.Parallel()

	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{Level: slog.LevelDebug}, telemetry.LogExtractor())
	svc := NewService(openStore(map[string]*Order{"1": {ID: "1", Qty: 3}}))

	req := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
	req.Header.Set("traceparent", traceparent)
	newServer(svc, log).ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	if !strings.Contains(out, traceID) {
		t.Fatalf("the production access log carries no trace id, so telemetry.Server is not in newServer's chain:\n%s", out)
	}
	// Route and trace id on the SAME line: the ordering that R4 and R5 both
	// pinned, asserted here against the production wiring rather than a
	// locally assembled one.
	if !strings.Contains(out, "GET /orders/{id}") {
		t.Errorf("access log lost its route while carrying a trace id:\n%s", out)
	}
}
