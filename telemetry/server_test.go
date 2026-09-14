package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
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

// TestServerSpanNameDoesNotDoublePrependForMethodfulPattern pins the same
// name assertion as TestServerSpanIsNamedForTheRouteNotThePath but states the
// invariant it exists to protect explicitly: when the registered pattern
// already carries a method ("GET /orders/{id}"), the span name must equal the
// pattern verbatim, never "GET GET /orders/{id}".
func TestServerSpanNameDoesNotDoublePrependForMethodfulPattern(t *testing.T) {
	tp := &recordingTracerProvider{}
	req := httptest.NewRequest(http.MethodGet, "/orders/123", nil)

	serve(t, tp, &recordingMeterProvider{}, "GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, req)

	name := tp.recorded()[0].name
	if strings.Count(name, http.MethodGet) != 1 {
		t.Fatalf("span name = %q, method appears %d times, want exactly 1", name, strings.Count(name, http.MethodGet))
	}
	if name != "GET /orders/{id}" {
		t.Errorf("span name = %q, want %q", name, "GET /orders/{id}")
	}
}

// TestServerSpanNameGainsMethodForMethodlessPattern is the fix under test: a
// route registered WITHOUT a method (mux.Handle("/admin/{x}", h)) yields
// r.Pattern="/admin/{x}" with no method component at all. Naming the span
// r.Pattern verbatim in that case collapses every method to one trace UI
// span name -- OTel semantic conventions want "{method} {route}", so the
// method has to be added back for exactly the patterns that never carried
// one.
func TestServerSpanNameGainsMethodForMethodlessPattern(t *testing.T) {
	tp := &recordingTracerProvider{}
	req := httptest.NewRequest(http.MethodPut, "/admin/5", nil)

	serve(t, tp, &recordingMeterProvider{}, "/admin/{x}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, req)

	name := tp.recorded()[0].name
	if want := "PUT /admin/{x}"; name != want {
		t.Errorf("span name = %q, want %q", name, want)
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

func attrOf(m measurement, key string) (attribute.Value, bool) {
	for _, kv := range m.attrs.ToSlice() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestServerRecordsRequestDuration(t *testing.T) {
	mp := &recordingMeterProvider{}
	req := httptest.NewRequest(http.MethodGet, "/orders/123", nil)

	serve(t, &recordingTracerProvider{}, mp, "GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}, req)

	got := mp.records()
	if len(got) != 1 {
		t.Fatalf("expected 1 measurement, got %d", len(got))
	}
	m := got[0]
	if m.name != "http.server.request.duration" {
		t.Errorf("instrument = %q", m.name)
	}
	// Seconds, per semconv -- NOT the milliseconds httpserver.AccessLog logs.
	// A differently-scaled http.server.request.duration is silently wrong on
	// every shared dashboard.
	if m.value <= 0 || m.value > 5 {
		t.Errorf("duration = %v s, want a small positive number of seconds", m.value)
	}
	for key, want := range map[string]string{
		"http.request.method": http.MethodGet,
		"http.route":          "GET /orders/{id}",
	} {
		v, ok := attrOf(m, key)
		if !ok || v.AsString() != want {
			t.Errorf("attr %s = %v (present=%v), want %q", key, v, ok, want)
		}
	}
	if v, ok := attrOf(m, "http.response.status_code"); !ok || v.AsInt64() != http.StatusCreated {
		t.Errorf("attr http.response.status_code = %v (present=%v)", v, ok)
	}
}

// TestServerMetricOmitsRouteWhenUnmatched: the metric attribute set is where
// cardinality actually costs money. A 404 must not create a time series per
// scanned URL.
func TestServerMetricOmitsRouteWhenUnmatched(t *testing.T) {
	mp := &recordingMeterProvider{}
	req := httptest.NewRequest(http.MethodGet, "/no/such/thing/9f3a", nil)

	serve(t, &recordingTracerProvider{}, mp, "GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, req)

	m := mp.records()[0]
	if v, ok := attrOf(m, "http.route"); ok {
		t.Fatalf("http.route = %v on an unmatched request; expected it to be omitted", v)
	}
}

// TestServerRecordsDurationWhenTheHandlerPanics: a panicking request is
// exactly the one you want in the latency histogram.
func TestServerRecordsDurationWhenTheHandlerPanics(t *testing.T) {
	mp := &recordingMeterProvider{}
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)

	func() {
		defer func() { recover() }()
		serve(t, &recordingTracerProvider{}, mp, "/boom", func(w http.ResponseWriter, r *http.Request) {
			panic("handler exploded")
		}, req)
	}()

	got := mp.records()
	if len(got) != 1 {
		t.Fatalf("expected a measurement for the panicking request, got %d", len(got))
	}
	// No response was sent, so claiming a status would be an invention.
	if v, ok := attrOf(got[0], "http.response.status_code"); ok {
		t.Errorf("status_code = %v recorded for a request that sent no response", v)
	}
}

// TestServerInstrumentIsBuiltOncePerMiddleware, not once per request: an
// instrument created inside the handler would be a per-request allocation and
// a per-request meter call.
func TestServerInstrumentIsBuiltOncePerMiddleware(t *testing.T) {
	mp := &countingMeterProvider{}
	h := Server(Options{MeterProvider: mp, TracerProvider: &recordingTracerProvider{}})(http.NewServeMux())

	for range 3 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}
	if n := mp.histograms(); n != 1 {
		t.Fatalf("built %d histograms for 3 requests, want 1", n)
	}
}
