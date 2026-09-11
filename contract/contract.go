// Package contract holds the minimal type vocabulary that svcgen-generated
// code depends on. It imports only "context" and never anything else, so a
// generated service's non-stdlib dependency footprint is this package alone.
//
// The API surface is deliberately tiny and is frozen at v1. Every addition is
// a design review.
package contract

// APIV1 is a compile-time compatibility marker. Generated code references it.
// A breaking change to this module removes it, so generated code that has
// skewed from its generator fails at build time with a comprehensible error
// rather than misbehaving at runtime.
type APIV1 struct{}

// Coded is implemented by errors whose code is part of the API surface.
//
// The code, not a message, is what crosses the wire: it is a stable
// identifier a client uses as a translation key. Nothing in svcrt or svcgen
// produces human-readable prose for an error.
type Coded interface {
	error
	ErrorCode() string
}

// Detailed is implemented by coded errors that additionally carry the
// language-neutral values a client needs to render a message.
//
// Implementing it is optional; errors with nothing to substitute should
// implement Coded alone. Values must be scalars — strings, numbers, or bools —
// because a client substitutes them into a template it owns.
//
// Declare the implementation explicitly so a typo is caught at compile time:
//
//	var _ contract.Detailed = quantityErr{}
type Detailed interface {
	Coded
	ErrorParams() map[string]any
}
