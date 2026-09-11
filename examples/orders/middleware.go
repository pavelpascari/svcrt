package main

import (
	"context"

	"github.com/pavelpascari/svcrt/contract"
)

// The typed middleware in this file exists to put pressure on
// contract.Middleware before it freezes. It is the only R0 consumer of the
// generic seam, since generated code -- its real consumer -- does not exist
// until G0.
//
// Note what is NOT here: logging, tracing, and request IDs. Those need
// nothing from the decoded request, so they belong at transport level as
// plain func(http.Handler) http.Handler. Call-level middleware is for
// concerns that genuinely need the typed request.

// RejectEmptyID refuses a request whose ID never arrived. It needs the decoded
// request, which is what makes it call-level rather than transport-level.
func RejectEmptyID(next contract.Handler[GetOrderRequest, *Order]) contract.Handler[GetOrderRequest, *Order] {
	return func(ctx context.Context, req GetOrderRequest) (*Order, error) {
		if req.ID == "" {
			return nil, notFoundError{id: ""}
		}
		return next(ctx, req)
	}
}

// RejectLongID caps the id length and reports the limit and the received
// length as params, so a client can render the message in any language.
//
// Unlike RejectEmptyID this one is reachable over HTTP -- a {id} wildcard
// never matches an empty path segment, so an empty id cannot arrive through
// the route. That makes RejectLongID the middleware the acceptance test
// exercises end to end, including params on the wire.
func RejectLongID(max int) contract.Middleware[GetOrderRequest, *Order] {
	return func(next contract.Handler[GetOrderRequest, *Order]) contract.Handler[GetOrderRequest, *Order] {
		return func(ctx context.Context, req GetOrderRequest) (*Order, error) {
			if len(req.ID) > max {
				return nil, idTooLongError{Max: max, Got: len(req.ID)}
			}
			return next(ctx, req)
		}
	}
}

// CountCalls records how many requests reached the handler beneath it.
func CountCalls(n *int) contract.Middleware[GetOrderRequest, *Order] {
	return func(next contract.Handler[GetOrderRequest, *Order]) contract.Handler[GetOrderRequest, *Order] {
		return func(ctx context.Context, req GetOrderRequest) (*Order, error) {
			*n++
			return next(ctx, req)
		}
	}
}
