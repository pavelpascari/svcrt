package httpclient_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pavelpascari/svcrt/httpclient"
)

// record returns a Middleware that appends name to *log on the way in.
func record(log *[]string, name string) httpclient.Middleware {
	return func(next http.RoundTripper) http.RoundTripper {
		return httpclient.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			*log = append(*log, name)
			return next.RoundTrip(r)
		})
	}
}

// terminal is a RoundTripper that answers without touching the network.
func terminal(log *[]string) http.RoundTripper {
	return httpclient.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		*log = append(*log, "transport")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Request:    r,
		}, nil
	})
}

func TestChainAppliesFirstArgumentOutermost(t *testing.T) {
	t.Parallel()
	var log []string

	rt := httpclient.Chain(record(&log, "a"), record(&log, "b"))(terminal(&log))
	req, _ := http.NewRequest("GET", "http://example.invalid/", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	// Chain(a, b) must produce a(b(transport)): a sees the request first.
	// This is the same ordering httpserver.Chain documents, and the two
	// seams looking symmetrical while behaving differently would be worse
	// than them looking different.
	if got, want := strings.Join(log, ","), "a,b,transport"; got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestChainWithNoMiddlewareIsIdentity(t *testing.T) {
	t.Parallel()
	var log []string

	base := terminal(&log)
	rt := httpclient.Chain()(base)

	req, _ := http.NewRequest("GET", "http://example.invalid/", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	// Chain() is the identity, so it should just call terminal, resulting in ["transport"].
	// This confirms a caller can fold an empty slice without a special case.
	if got, want := strings.Join(log, ","), "transport"; got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestRoundTripperFuncCallsTheFunction(t *testing.T) {
	t.Parallel()
	called := false
	rt := httpclient.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusTeapot, Body: http.NoBody}, nil
	})

	req, _ := http.NewRequest("GET", "http://example.invalid/", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if !called {
		t.Error("RoundTrip did not call the function")
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
}
