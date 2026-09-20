package httpclient_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/pavelpascari/svcrt/httpclient"
)

// ExampleNew builds a client with a middleware that stamps every outbound
// request. What comes back is the stdlib *http.Client, so it drops into every
// API that expects one -- but assigning c.Transport afterwards would discard
// this configuration and the middleware with it.
func ExampleNew() {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("upstream saw authorization:", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	// Clone before mutating: a RoundTripper must not modify the request it
	// was handed, and a retry may hand it the same one again.
	auth := func(next http.RoundTripper) http.RoundTripper {
		return httpclient.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer t0ken")
			return next.RoundTrip(r)
		})
	}

	c := httpclient.New(httpclient.Options{
		DialTimeout: 2 * time.Second,
		Middleware:  auth,
	})

	resp, err := c.Get(upstream.URL + "/orders")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	defer resp.Body.Close()

	fmt.Println("status:", resp.StatusCode)

	// The client owns no lifecycle. To release idle connections at shutdown,
	// register this with your lifecycle's stop function.
	c.CloseIdleConnections()

	// Output:
	// upstream saw authorization: Bearer t0ken
	// status: 204
}

// ExampleChain folds several middlewares into one. The first argument ends up
// outermost, so it sees the request first and the response last.
func ExampleChain() {
	trace := func(name string) httpclient.Middleware {
		return func(next http.RoundTripper) http.RoundTripper {
			return httpclient.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				fmt.Println(name, "->")
				resp, err := next.RoundTrip(r)
				fmt.Println(name, "<-")
				return resp, err
			})
		}
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	c := httpclient.New(httpclient.Options{
		Middleware: httpclient.Chain(trace("outer"), trace("inner")),
	})

	resp, err := c.Get(upstream.URL)
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	resp.Body.Close()

	// Output:
	// outer ->
	// inner ->
	// inner <-
	// outer <-
}
