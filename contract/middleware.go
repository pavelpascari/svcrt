package contract

import "context"

// Handler is a single decoded, typed call. Generated server code adapts a
// transport request into one of these; generated client code implements one.
type Handler[Req, Res any] func(ctx context.Context, req Req) (Res, error)

// Middleware wraps a Handler.
//
// It is generic rather than boxed through any so that wrapping costs no
// allocation on the request path. The trade-off is that a Middleware applies
// to one method signature, not to all methods generically. That is deliberate:
// concerns that do not need the decoded request -- logging, tracing, auth,
// rate limiting -- belong at transport level, where they are already just
// func(http.Handler) http.Handler and need nothing from this package.
type Middleware[Req, Res any] func(Handler[Req, Res]) Handler[Req, Res]

// Chain folds ms into a single Middleware. The first argument ends up
// outermost, matching net/http middleware convention: Chain(a, b) produces
// a(b(next)), so a observes the call first and returns last.
//
// Chain with no arguments returns the identity middleware, so callers can fold
// an empty slice without a special case.
func Chain[Req, Res any](ms ...Middleware[Req, Res]) Middleware[Req, Res] {
	return func(next Handler[Req, Res]) Handler[Req, Res] {
		for i := len(ms) - 1; i >= 0; i-- {
			next = ms[i](next)
		}
		return next
	}
}
