package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/pavelpascari/svcrt/httpserver"
)

func tag(log *[]string, name string) httpserver.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*log = append(*log, name+":before")
			next.ServeHTTP(w, r)
			*log = append(*log, name+":after")
		})
	}
}

func TestChainAppliesFirstArgumentOutermost(t *testing.T) {
	t.Parallel()
	var log []string
	base := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		log = append(log, "handler")
	})

	h := httpserver.Chain(tag(&log, "a"), tag(&log, "b"))(base)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	want := []string{"a:before", "b:before", "handler", "b:after", "a:after"}
	if !slices.Equal(log, want) {
		t.Errorf("order = %v, want %v", log, want)
	}
}

func TestChainWithNoMiddlewareIsIdentity(t *testing.T) {
	t.Parallel()
	called := false
	base := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })

	httpserver.Chain()(base).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if !called {
		t.Error("Chain() did not pass the request through")
	}
}

func TestChainMiddlewareCanShortCircuit(t *testing.T) {
	t.Parallel()
	reached := false
	deny := httpserver.Middleware(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
	})
	base := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

	rec := httptest.NewRecorder()
	httpserver.Chain(deny)(base).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if reached {
		t.Error("the base handler ran despite a short-circuit")
	}
}

func TestChainDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()
	var log []string
	ms := []httpserver.Middleware{tag(&log, "a"), tag(&log, "b")}

	// Func values aren't comparable with ==, so identity is captured via the
	// closure's underlying pointer. This catches Chain replacing an element
	// with a different (even non-nil) middleware, which a nilness-only
	// comparison cannot.
	before := make([]uintptr, len(ms))
	for i, m := range ms {
		before[i] = reflect.ValueOf(m).Pointer()
	}

	_ = httpserver.Chain(ms...)

	for i, m := range ms {
		if got := reflect.ValueOf(m).Pointer(); got != before[i] {
			t.Fatalf("Chain modified element %d of the slice it was given", i)
		}
	}
}
