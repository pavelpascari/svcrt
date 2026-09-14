package httpserver_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pavelpascari/svcrt/httpserver"
)

func captureServe(t *testing.T, mux http.Handler, req *http.Request) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := httpserver.AccessLog(log)(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	line := map[string]any{}
	if buf.Len() == 0 {
		t.Fatal("middleware emitted no log line")
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("decode log line %q: %v", buf.String(), err)
	}
	return rec, line
}

func TestAccessLogLogsRoutePatternNotPath(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	_, line := captureServe(t, mux, httptest.NewRequest("GET", "/orders/8a3f-not-a-route", nil))

	if line[httpserver.KeyRoute] != "GET /orders/{id}" {
		t.Errorf("%s = %v, want the pattern", httpserver.KeyRoute, line[httpserver.KeyRoute])
	}
	if line[httpserver.KeyMethod] != "GET" {
		t.Errorf("%s = %v, want GET", httpserver.KeyMethod, line[httpserver.KeyMethod])
	}
	if line[httpserver.KeyStatus] != float64(200) {
		t.Errorf("%s = %v, want 200", httpserver.KeyStatus, line[httpserver.KeyStatus])
	}
	if _, ok := line[httpserver.KeyDurMS]; !ok {
		t.Errorf("%s missing from %v", httpserver.KeyDurMS, line)
	}
}

// A high-cardinality path must never reach the log line, even on a 404, or a
// scanner can inflate log volume at will.
func TestAccessLogOmitsRouteWhenNoPatternMatched(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {})

	_, line := captureServe(t, mux, httptest.NewRequest("GET", "/no-such-route-8a3f", nil))

	if v, ok := line[httpserver.KeyRoute]; ok {
		t.Errorf("%s = %v, want it omitted on an unmatched request", httpserver.KeyRoute, v)
	}
	for k, v := range line {
		if s, isStr := v.(string); isStr && s == "/no-such-route-8a3f" {
			t.Errorf("raw path leaked into key %q", k)
		}
	}
	if line[httpserver.KeyStatus] != float64(404) {
		t.Errorf("%s = %v, want 404", httpserver.KeyStatus, line[httpserver.KeyStatus])
	}
}

func TestAccessLogRecordsExplicitStatus(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	rec, line := captureServe(t, mux, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("recorder code = %d, want 418", rec.Code)
	}
	if line[httpserver.KeyStatus] != float64(418) {
		t.Errorf("%s = %v, want 418", httpserver.KeyStatus, line[httpserver.KeyStatus])
	}
}

func TestAccessLogDefaultsToStatus200OnImplicitWrite(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body")) // no WriteHeader call
	})

	_, line := captureServe(t, mux, httptest.NewRequest("GET", "/x", nil))

	if line[httpserver.KeyStatus] != float64(200) {
		t.Errorf("%s = %v, want 200", httpserver.KeyStatus, line[httpserver.KeyStatus])
	}
}

// A WriteHeader call after an implicit write (the body already went out
// under the implicit 200) is superfluous -- the real header was already
// sent. The wrapper must keep recording the first, implicit status rather
// than letting a later WriteHeader call overwrite it, which requires an
// implicit Write to mark the writer as written just as an explicit
// WriteHeader does.
func TestAccessLogIgnoresWriteHeaderAfterImplicitWrite(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body")) // implicit 200, marks the writer written
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, line := captureServe(t, mux, httptest.NewRequest("GET", "/x", nil))

	if line[httpserver.KeyStatus] != float64(200) {
		t.Errorf("%s = %v, want 200 (superfluous WriteHeader must not overwrite it)", httpserver.KeyStatus, line[httpserver.KeyStatus])
	}
}

// A naive ResponseWriter wrapper silently breaks SSE and connection upgrades.
// The wrapper must stay transparent to http.ResponseController.
func TestAccessLogPreservesFlusherThroughResponseController(t *testing.T) {
	t.Parallel()

	var flushErr error
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("chunk"))
		flushErr = http.NewResponseController(w).Flush()
	})

	captureServe(t, mux, httptest.NewRequest("GET", "/stream", nil))

	if flushErr != nil {
		t.Errorf("Flush through the wrapper failed: %v", flushErr)
	}
}

func TestAccessLogPassesRequestThrough(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello body"))
	})

	rec, _ := captureServe(t, mux, httptest.NewRequest("GET", "/x", nil))

	if got := rec.Body.String(); got != "hello body" {
		t.Errorf("body = %q, want %q", got, "hello body")
	}
}

// The request you most want in the access log is the one that blew up.
// LogAttrs used not to be deferred, so a panicking handler skipped it
// entirely and the crash left no line at all.
func TestAccessLogLogsWhenTheHandlerPanics(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom")
	})
	h := httpserver.AccessLog(log)(mux)

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/boom", nil))
	}()

	// The middleware must observe the panic, not swallow it: recovery is the
	// service author's decision, made in their own middleware.
	if recovered != "kaboom" {
		t.Fatalf("recovered = %v, want the panic to propagate", recovered)
	}

	if buf.Len() == 0 {
		t.Fatal("no log line emitted for a panicking handler")
	}
	line := map[string]any{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("decode log line %q: %v", buf.String(), err)
	}
	if line[httpserver.KeyRoute] != "GET /boom" {
		t.Errorf("%s = %v, want %q", httpserver.KeyRoute, line[httpserver.KeyRoute], "GET /boom")
	}
	if line[httpserver.KeyMethod] != "GET" {
		t.Errorf("%s = %v, want GET", httpserver.KeyMethod, line[httpserver.KeyMethod])
	}
	// Nothing wrote a header, and net/http sends no response at all on a
	// panic, so the constructor's optimistic 200 default never happened.
	// Reporting it -- or inventing a 500 -- would send an on-call engineer
	// chasing the wrong thing. The status attribute must be absent.
	if _, ok := line[httpserver.KeyStatus]; ok {
		t.Errorf("%s = %v, want absent (nothing was written)", httpserver.KeyStatus, line[httpserver.KeyStatus])
	}
}
