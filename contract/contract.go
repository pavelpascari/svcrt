// Package contract holds the minimal type vocabulary that svcgen-generated
// code depends on. It imports only "context" and never anything else, so a
// generated service's non-stdlib dependency footprint is this package alone.
//
// The API surface is deliberately tiny, and the intent is to freeze it at v1.
// That freeze has not happened yet: contract is not tagged v1, and the
// hand-written middleware in examples/orders exists to put the generic seam
// under real use before its shape becomes permanent.
//
// Until the freeze is decided, treat every addition as a design review. What
// goes in now is what a frozen v1 would have to carry forever: removing an
// exported name after v1 is a major version bump on contract and on every
// module that names one of its types.
package contract

// KeyCode is the well-known log attribute key for a Coded error's code.
//
// It lives here rather than in a logging package because it names this
// package's vocabulary: the value a caller puts under it is what Coded.Code
// returns. A server-side log line and a client-side error can then be joined
// on the same field.
const KeyCode = "code"

// APIV1 is a compile-time marker that generated code references.
//
// What it detects is narrower than it looks, and worth writing down before
// the freeze makes it unremovable. It does NOT guard against a generated
// service building against an incompatible contract module: Go's own rules
// already prevent that, because a breaking change here becomes contract/v2
// with a different import path, which no existing generated code can
// accidentally resolve to. The marker earns its place on the other axis --
// generator generations inside a permanently-v1 module. A later generator
// whose emitted code needs something this one does not will reference an
// APIV2 declared alongside this type, so code from the newer generator fails
// against an older contract with "undefined: contract.APIV2" rather than
// misbehaving at runtime.
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
// That constraint is a convention, not compiler-enforced, and that is
// deliberate. Go cannot express a scalar type-union as a map value type, so
// the honest options were map[string]any or a hand-rolled Param wrapper type
// at every call site. The wrapper was rejected because it does not catch the
// violation that actually happens: the realistic mistake is not returning a
// struct, it is returning {"reason": "quantity is too high"} — a perfectly
// scalar string that is also display prose, which no signature can reject.
// Given that the dangerous case is uncatchable by types either way, the
// simpler type wins.
//
// Today the convention is enforced by the type-switch test over the error
// type (see TestIDTooLongErrorCarriesScalarParams in examples/orders) plus
// review — nothing in svcrt validates it at runtime. Once generated code
// exists, enforcement belongs at the emitted serialization boundary, which
// can reject a non-scalar value there instead.
//
// Declare the implementation explicitly so a typo is caught at compile time:
//
//	var _ contract.Detailed = quantityErr{}
type Detailed interface {
	Coded
	ErrorParams() map[string]any
}
