package httpserver

import "net/http"

// Middleware wraps an http.Handler.
//
// Note the deliberate spelling difference across this project, recorded in
// docs/conventions.md: contract.Middleware is a generic call-level type,
// httpserver.Middleware (this one) wraps an http.Handler, and
// httpclient.Middleware wraps an http.RoundTripper.
type Middleware func(http.Handler) http.Handler

// Chain folds ms into one Middleware. The first argument ends up outermost,
// matching net/http convention: Chain(a, b) produces a(b(next)), so a observes
// the request first and the response last.
//
// Chain with no arguments returns the identity middleware, so a caller can fold
// an empty slice without a special case.
func Chain(ms ...Middleware) Middleware {
	return func(next http.Handler) http.Handler {
		for i := len(ms) - 1; i >= 0; i-- {
			next = ms[i](next)
		}
		return next
	}
}
