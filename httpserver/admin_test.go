package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pavelpascari/svcrt/httpserver"
)

func TestAdminMuxMountsTheThreeProbes(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	h.Started.Set(true)
	h.Live.Set(true)
	h.Ready.Set(true)
	mux := httpserver.AdminMux(h)

	for path, want := range map[string]int{
		"/healthz": http.StatusOK, "/readyz": http.StatusOK, "/startupz": http.StatusOK,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}

func TestAdminMuxRoutesEachPathToItsOwnGate(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	h.Started.Set(true)
	h.Live.Set(true)
	h.Ready.Set(false) // only readiness is down

	mux := httpserver.AdminMux(h)
	for path, want := range map[string]int{
		"/healthz":  http.StatusOK,
		"/startupz": http.StatusOK,
		"/readyz":   http.StatusServiceUnavailable,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}

func TestAdminMuxIsExtensible(t *testing.T) {
	t.Parallel()
	mux := httpserver.AdminMux(httpserver.NewHealth())
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("added route = %d, want 418", rec.Code)
	}
}

// Importing httpserver must NOT drag net/http/pprof in, because that package's
// init() registers handlers on http.DefaultServeMux for every consumer.
func TestImportingHttpserverDoesNotPolluteDefaultServeMux(t *testing.T) {
	rec := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/", nil))
	if rec.Code == http.StatusOK {
		t.Error("importing httpserver registered pprof on http.DefaultServeMux; " +
			"it must not import net/http/pprof")
	}
}
