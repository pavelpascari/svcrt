// Package httpclient builds an http.Client that is configured correctly, with
// a middleware seam mirroring httpserver's.
//
// It never reads or mutates http.DefaultClient or http.DefaultTransport. See
// New for why that matters.
package httpclient

import "net/http"

// Middleware wraps an http.RoundTripper.
//
// This is the client-side mirror of httpserver.Middleware. svcrt spells
// Middleware three ways on purpose, one per seam: contract.Middleware is a
// generic call-level type over a decoded request, httpserver.Middleware wraps
// an http.Handler, and this wraps an http.RoundTripper. They are different
// types, not interchangeable ones.
//
// Because http.RoundTripper is stdlib vocabulary, a module that ships
// middleware of this shape needs no dependency on httpclient at all.
type Middleware func(http.RoundTripper) http.RoundTripper

// Chain folds ms into one Middleware. The first argument ends up outermost,
// matching net/http convention and httpserver.Chain: Chain(a, b) produces
// a(b(next)), so a observes the request first and the response last.
//
// Chain with no arguments returns the identity middleware, so a caller can fold
// an empty slice without a special case.
func Chain(ms ...Middleware) Middleware {
	return func(next http.RoundTripper) http.RoundTripper {
		for i := len(ms) - 1; i >= 0; i-- {
			next = ms[i](next)
		}
		return next
	}
}

// RoundTripperFunc adapts a function to http.RoundTripper.
//
// The stdlib has http.HandlerFunc but no RoundTripper equivalent, so every
// middleware author otherwise writes this same adapter. Exported so they do
// not have to.
type RoundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip calls f.
func (f RoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
