package pprof_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pavelpascari/svcrt/httpserver/pprof"
)

func TestHandlerServesTheIndex(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	pprof.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /debug/pprof/ = %d, want 200", rec.Code)
	}
}

func TestHandlerServesANamedProfile(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	pprof.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/goroutine?debug=1", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /debug/pprof/goroutine = %d, want 200", rec.Code)
	}
}

func TestHandlerServesCmdline(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	pprof.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/cmdline", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /debug/pprof/cmdline = %d, want 200", rec.Code)
	}
}

// The full profile, symbol, and trace handlers are not invoked here: profile
// defaults to a 30-second CPU capture and trace to a 1-second one when no
// "seconds" param is given, and that cost buys nothing a route-registration
// check does not already prove. Instead this asks the mux which handler it
// would use, without running it, and confirms each path resolves to its own
// exact-match pattern rather than falling back to the catch-all
// "/debug/pprof/" pattern (which would still be net/http/pprof.Index, and
// would silently 404 for "profile").
func TestHandlerRegistersProfileSymbolAndTraceRoutes(t *testing.T) {
	t.Parallel()
	mux, ok := pprof.Handler().(*http.ServeMux)
	if !ok {
		t.Fatalf("Handler() returned %T, want *http.ServeMux", pprof.Handler())
	}
	for _, path := range []string{"/debug/pprof/profile", "/debug/pprof/symbol", "/debug/pprof/trace"} {
		_, pattern := mux.Handler(httptest.NewRequest("GET", path, nil))
		if pattern != path {
			t.Errorf("route for %s resolved to pattern %q, want an exact match on %q", path, pattern, path)
		}
	}
}

func TestHandlerMountsUnderAPrefixWithoutLosingRoutes(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.Handle("/debug/pprof/", pprof.Handler())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/cmdline", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("mounted GET /debug/pprof/cmdline = %d, want 200", rec.Code)
	}
}
