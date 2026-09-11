package config

import (
	"errors"
	"reflect"
)

// Option configures a Load call.
type Option func(*options)

type options struct {
	src    Source
	prefix string
}

// WithSource sets where values are read from. The default is OSEnv().
//
// Tests should pass a Source backed by a map rather than mutating the process
// environment, so they can run in parallel.
func WithSource(s Source) Option { return func(o *options) { o.src = s } }

// WithPrefix prepends p to every environment key, ahead of any envPrefix
// chain. Prefixes are concatenated verbatim, so p must carry its own trailing
// separator: WithPrefix("APP_") with envPrefix:"OTEL_" and env:"ENDPOINT"
// reads APP_OTEL_ENDPOINT.
func WithPrefix(p string) Option { return func(o *options) { o.prefix = p } }

// validatable is implemented by config types that carry rules a tag cannot
// express -- ranges, mutual exclusion, cross-field dependencies.
//
// Load calls Validate only when every field decoded successfully, so the
// method never sees a partially populated struct.
type validatable interface {
	Validate() error
}

// Load decodes T from the environment, validates it, and returns it.
//
// Every field must carry an env tag (or envPrefix, for a nested struct);
// there is no inference from field names.
//
// A field is required unless something makes it optional, and two things do:
// a default: tag, which supplies a value when the variable is absent, and a
// pointer type, whose nil zero value already means "not set". A non-pointer
// field with no default: tag is required, and its absence fails the load.
//
// Pointers are optional at two levels, and the two differ in one way worth
// knowing before it surprises you:
//
//   - a default: on a pointer SCALAR materializes it. *int with
//     default:"3" is a non-nil pointer to 3 when the variable is unset.
//   - a default: on a field inside a pointer STRUCT block does NOT
//     materialize the block. A block exists only when the Source actually
//     holds one of its keys; otherwise it stays nil and the defaults inside
//     it never apply. If defaults counted as presence, a block containing
//     any defaulted field could never be absent, which is the whole point of
//     an optional block.
//
// Both are intentional. A pointer scalar's default is the value to use when
// the operator said nothing; an optional block's defaults are the values to
// use once the operator has opted the block in.
//
// Load reports every problem it finds at once rather than stopping at the
// first, so an operator sees the whole list in one restart. On any error it
// returns the zero value of T and a *Error.
//
// All reflection happens here, on the startup path. The returned value is a
// copy: there is no handle to mutate, no reload, and no watch.
func Load[T any](opts ...Option) (T, error) {
	var zero, v T

	o := options{src: OSEnv()}
	for _, opt := range opts {
		opt(&o)
	}

	rv := reflect.ValueOf(&v).Elem()

	// Stage 1: plan. Schema violations make the type unloadable under any
	// environment, so reporting missing values alongside them would be noise.
	p, schemaViolations := buildPlan(rv.Type(), o.prefix)
	if len(schemaViolations) > 0 {
		return zero, &Error{Violations: schemaViolations}
	}

	// Stage 2: resolve. Look every key up before deciding anything, so block
	// presence can be computed from the complete picture.
	type resolved struct {
		raw   string
		found bool
	}
	res := make([]resolved, len(p.bindings))
	for i, b := range p.bindings {
		raw, found := o.src(b.env)
		res[i] = resolved{raw: raw, found: found}
	}

	// An optional block exists only if something in its subtree is actually
	// set. Defaults do not count: if they did, a block holding any defaulted
	// field could never be absent.
	skip := make([]bool, len(p.bindings))
	for _, blk := range p.blocks {
		present := false
		for i, b := range p.bindings {
			if underBlock(b.index, blk.index) && res[i].found {
				present = true
				break
			}
		}
		if present {
			continue
		}
		for i, b := range p.bindings {
			if underBlock(b.index, blk.index) {
				skip[i] = true
			}
		}
	}

	// Stage 3: decode.
	var violations []Violation
	for i, b := range p.bindings {
		if skip[i] {
			continue
		}

		raw := res[i].raw
		switch {
		case res[i].found:
			// Present, including when explicitly empty.
		case b.hasDef:
			raw = b.def
		case b.ptr:
			// A nil pointer already means "not set"; no default: tag is
			// needed to make a pointer field optional.
			continue
		default:
			violations = append(violations, Violation{
				Env: b.env, Field: b.field, Kind: KindRequired,
				Err: errors.New("required, not set"),
			})
			continue
		}

		dst := fieldByIndexAlloc(rv, b.index)
		if err := b.dec(raw, dst); err != nil {
			violations = append(violations, Violation{
				Env: b.env, Field: b.field, Kind: KindDecode, Err: err,
			})
		}
	}

	if len(violations) > 0 {
		return zero, &Error{Violations: violations}
	}

	// Stage 4: validate. Only reached with a fully decoded struct.
	if val, ok := any(&v).(validatable); ok {
		if err := val.Validate(); err != nil {
			return zero, &Error{Violations: []Violation{{
				Field: rv.Type().Name(), Kind: KindValidate, Err: err,
			}}}
		}
	}

	return v, nil
}
