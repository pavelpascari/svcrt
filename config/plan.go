package config

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
)

// binding is one environment variable mapped to one field.
type binding struct {
	index  []int  // field index path from the root struct
	field  string // dotted Go path, e.g. "AppConfig.OTel.Endpoint"
	env    string // full environment key, prefixes already applied
	def    string // value from the default: tag
	hasDef bool   // whether a default: tag was present

	// ptr reports whether the field's own type is a pointer. A pointer's
	// zero value (nil) already means "not set", so unlike a string or int
	// there is no need for a default: tag to make it optional -- absence
	// just leaves the pointer nil.
	ptr bool

	dec decoder
}

// block is a *Struct field: an optional group of bindings that is only
// materialized when something in its subtree is set.
type block struct {
	index []int
	field string
}

// plan is the reflection result for one config type. Building it is the only
// place config uses reflect, and it happens once, on the startup path.
type plan struct {
	bindings []binding
	blocks   []block
}

// buildPlan walks t and produces every binding and optional block, or the
// complete list of schema violations preventing that.
//
// prefix is prepended to every key, ahead of any envPrefix chain.
func buildPlan(t reflect.Type, prefix string) (*plan, []Violation) {
	if t.Kind() != reflect.Struct {
		return nil, []Violation{{
			Field: t.String(),
			Kind:  KindSchema,
			Err:   fmt.Errorf("config type must be a struct, got %s", t.Kind()),
		}}
	}

	w := &walker{
		p:      &plan{},
		seen:   map[string]string{},
		active: []reflect.Type{t},
	}
	w.walk(t, t.Name(), prefix, nil)
	return w.p, w.violations
}

type walker struct {
	p          *plan
	violations []Violation
	seen       map[string]string // env key -> field path that claimed it

	// active is the stack of struct types on the current walk path, used to
	// refuse a cyclic config type. Without it `type Node struct { Next *Node
	// \`envPrefix:"N_"\` }` recurses forever: each level appends a binding
	// rather than growing the stack, so there is no panic -- just a process
	// that never boots, which is the opposite of this module's contract.
	//
	// It is a path stack rather than a set of every type ever seen, because
	// reusing one config fragment at two different prefixes is the documented
	// way to share a fragment (§5.4) and must keep working.
	active []reflect.Type
}

func (w *walker) add(v Violation) { w.violations = append(w.violations, v) }

func (w *walker) walk(t reflect.Type, path, prefix string, index []int) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}

		fieldPath := path + "." + f.Name
		fieldIndex := append(slices.Clip(index), i)

		envTag, hasEnv := f.Tag.Lookup("env")
		if hasEnv && envTag == "-" {
			continue
		}

		prefixTag, hasPrefix := f.Tag.Lookup("envPrefix")

		if hasEnv && hasPrefix {
			w.add(Violation{Field: fieldPath, Kind: KindSchema,
				Err: errors.New(`has both env and envPrefix; a field is either a value or a nested group`)})
			continue
		}

		if isNestedStruct(f.Type) {
			if !hasPrefix {
				w.add(Violation{Field: fieldPath, Kind: KindSchema,
					Err: errors.New(`nested struct needs an envPrefix tag; use envPrefix:"" to embed flat`)})
				continue
			}
			inner := f.Type
			if inner.Kind() == reflect.Pointer {
				inner = inner.Elem()
			}
			if slices.Contains(w.active, inner) {
				w.add(Violation{Field: fieldPath, Kind: KindSchema,
					Err: fmt.Errorf("cyclic config type: %s contains itself", inner)})
				continue
			}
			if f.Type.Kind() == reflect.Pointer {
				w.p.blocks = append(w.p.blocks, block{index: fieldIndex, field: fieldPath})
			}
			w.active = append(w.active, inner)
			w.walk(inner, fieldPath, prefix+prefixTag, fieldIndex)
			w.active = w.active[:len(w.active)-1]
			continue
		}

		if !hasEnv {
			w.add(Violation{Field: fieldPath, Kind: KindSchema,
				Err: errors.New(`missing env tag; use env:"-" to skip this field`)})
			continue
		}
		if envTag == "" {
			w.add(Violation{Field: fieldPath, Kind: KindSchema,
				Err: errors.New("env tag is empty")})
			continue
		}

		key := prefix + envTag
		if owner, dup := w.seen[key]; dup {
			w.add(Violation{Env: key, Field: fieldPath, Kind: KindSchema,
				Err: fmt.Errorf("environment key already bound by %s", owner)})
			continue
		}

		dec, err := decoderFor(f.Type)
		if err != nil {
			w.add(Violation{Env: key, Field: fieldPath, Kind: KindSchema, Err: err})
			continue
		}

		def, hasDef := f.Tag.Lookup("default")
		w.seen[key] = fieldPath
		w.p.bindings = append(w.p.bindings, binding{
			index:  fieldIndex,
			field:  fieldPath,
			env:    key,
			def:    def,
			hasDef: hasDef,
			ptr:    f.Type.Kind() == reflect.Pointer,
			dec:    dec,
		})
	}
}

// isNestedStruct reports whether t is a struct (or pointer to one) that should
// be walked rather than decoded from a single value.
//
// A struct that parses itself -- time.Time, netip.Addr, any TextUnmarshaler --
// is a value, not a group, so it is excluded here and handled by decoderFor.
func isNestedStruct(t reflect.Type) bool {
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return false
	}
	switch t.Kind() {
	case reflect.Struct:
		return true
	case reflect.Pointer:
		e := t.Elem()
		return e.Kind() == reflect.Struct && !reflect.PointerTo(e).Implements(textUnmarshalerType)
	default:
		return false
	}
}

// underBlock reports whether a binding at bindingIndex lives inside the block
// at blockIndex. Index paths make this a prefix test.
func underBlock(bindingIndex, blockIndex []int) bool {
	if len(bindingIndex) <= len(blockIndex) {
		return false
	}
	return slices.Equal(bindingIndex[:len(blockIndex)], blockIndex)
}

// fieldByIndexAlloc walks path from v, allocating any nil pointer it passes
// through, and returns the settable field at the end.
//
// reflect.Value.FieldByIndex panics on a nil intermediate pointer, so this
// exists to materialize optional blocks on demand.
func fieldByIndexAlloc(v reflect.Value, path []int) reflect.Value {
	for _, i := range path {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v
}
