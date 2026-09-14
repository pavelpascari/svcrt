package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const inboundTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
const inboundTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

// serve runs one request through Server-wrapped mux and returns the recorder.
func serve(t *testing.T, tp *recordingTracerProvider, mp *recordingMeterProvider, pattern string, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(pattern, h)

	rec := httptest.NewRecorder()
	Server(Options{TracerProvider: tp, MeterProvider: mp})(mux).ServeHTTP(rec, req)
	return rec
}

func TestServerContinuesAnInboundTrace(t *testing.T) {
	tp := &recordingTracerProvider{}
	req := httptest.NewRequest(http.MethodGet, "/orders/123", nil)
	req.Header.Set("traceparent", inboundTraceparent)

	var seen trace.SpanContext
	serve(t, tp, &recordingMeterProvider{}, "GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		seen = trace.SpanContextFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}, req)

	if !seen.IsValid() {
		t.Fatal("handler saw no valid span context")
	}
	if got := seen.TraceID().String(); got != inboundTraceID {
		t.Errorf("trace id = %q, want the inbound one %q", got, inboundTraceID)
	}
}

// TestServerSpanIsNamedForTheRouteNotThePath is the cardinality guard. The
// route is only known after the mux has matched, so the name is set on the way
// out -- reading r.Pattern before next.ServeHTTP yields "".
func TestServerSpanIsNamedForTheRouteNotThePath(t *testing.T) {
	tp := &recordingTracerProvider{}
	req := httptest.NewRequest(http.MethodGet, "/orders/123", nil)

	serve(t, tp, &recordingMeterProvider{}, "GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, req)

	spans := tp.recorded()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if got := spans[0].name; got != "GET /orders/{id}" {
		t.Errorf("span name = %q, want %q", got, "GET /orders/{id}")
	}
	if got := spans[0].kind; got != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", got)
	}
}

// TestServerUnmatchedRouteDoesNotNameTheSpanForThePath: a 404 has no pattern.
// Falling back to the raw path would reintroduce unbounded cardinality on
// exactly the URLs a scanner can generate at will.
func TestServerUnmatchedRouteDoesNotNameTheSpanForThePath(t *testing.T) {
	tp := &recordingTracerProvider{}
	req := httptest.NewRequest(http.MethodGet, "/no/such/thing/9f3a", nil)

	serve(t, tp, &recordingMeterProvider{}, "GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, req)

	name := tp.recorded()[0].name
	if strings.Contains(name, "9f3a") {
		t.Fatalf("span name %q contains the raw path", name)
	}
	if name != http.MethodGet {
		t.Errorf("span name = %q, want the bare method %q", name, http.MethodGet)
	}
}

func TestServerMarksFiveHundredsAsErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   codes.Code
	}{
		{http.StatusOK, codes.Unset},
		{http.StatusNotFound, codes.Unset}, // 4xx is the caller's fault, not this server's
		{http.StatusInternalServerError, codes.Error},
		{http.StatusBadGateway, codes.Error},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			tp := &recordingTracerProvider{}
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			serve(t, tp, &recordingMeterProvider{}, "/x", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}, req)

			s := tp.recorded()[0]
			if s.code != tc.want {
				t.Errorf("status %d -> span code %v, want %v", tc.status, s.code, tc.want)
			}
			if v, ok := s.attr("http.response.status_code"); !ok || v.AsInt64() != int64(tc.status) {
				t.Errorf("span status_code attr = %v (present=%v), want %d", v, ok, tc.status)
			}
		})
	}
}

// TestServerEndsTheSpanWhenTheHandlerPanics: the request that crashed is the
// one most worth having a span for. The panic is deliberately not recovered --
// whether it becomes a 500 is the service author's decision -- so the test
// re-panics after asserting.
func TestServerEndsTheSpanWhenTheHandlerPanics(t *testing.T) {
	tp := &recordingTracerProvider{}
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic did not propagate; Server must not recover it")
			}
		}()
		serve(t, tp, &recordingMeterProvider{}, "/boom", func(w http.ResponseWriter, r *http.Request) {
			panic("handler exploded")
		}, req)
	}()

	spans := tp.recorded()
	if len(spans) != 1 || !spans[0].ended {
		t.Fatal("span was not ended when the handler panicked")
	}
}

// TestServerDefaultsToStatus200OnImplicitWrite exercises statusWriter.Write's
// no-WriteHeader path, mirroring httpserver.AccessLog's own writer.
func TestServerDefaultsToStatus200OnImplicitWrite(t *testing.T) {
	tp := &recordingTracerProvider{}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	serve(t, tp, &recordingMeterProvider{}, "/x", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body")) // no WriteHeader call
	}, req)

	s := tp.recorded()[0]
	if v, ok := s.attr("http.response.status_code"); !ok || v.AsInt64() != http.StatusOK {
		t.Errorf("status_code = %v (present=%v), want 200", v, ok)
	}
}

// TestServerIgnoresWriteHeaderAfterImplicitWrite: a WriteHeader call after an
// implicit write is superfluous -- the real header already went out under the
// implicit 200 -- so the wrapper must not let it overwrite the recorded
// status. This is what requires an implicit Write to mark the writer written
// just as an explicit WriteHeader does.
func TestServerIgnoresWriteHeaderAfterImplicitWrite(t *testing.T) {
	tp := &recordingTracerProvider{}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	serve(t, tp, &recordingMeterProvider{}, "/x", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body")) // implicit 200, marks the writer written
		w.WriteHeader(http.StatusInternalServerError)
	}, req)

	s := tp.recorded()[0]
	if v, ok := s.attr("http.response.status_code"); !ok || v.AsInt64() != http.StatusOK {
		t.Errorf("status_code = %v (present=%v), want 200 (superfluous WriteHeader must not overwrite it)", v, ok)
	}
}

// TestServerPreservesFlusherThroughResponseController: a naive ResponseWriter
// wrapper silently breaks SSE and connection upgrades. statusWriter must stay
// transparent to http.ResponseController via Unwrap.
func TestServerPreservesFlusherThroughResponseController(t *testing.T) {
	tp := &recordingTracerProvider{}
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)

	var flushErr error
	serve(t, tp, &recordingMeterProvider{}, "/stream", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("chunk"))
		flushErr = http.NewResponseController(w).Flush()
	}, req)

	if flushErr != nil {
		t.Errorf("Flush through the wrapper failed: %v", flushErr)
	}
}
