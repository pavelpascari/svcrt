# Mutation Test Survivors

This document records mutants that survive the mutation testing threshold for each module. Modules with perfect coverage (all mutants killed) have no entry.

## `config` Module

**Mutation Score: above threshold, with 3 surviving mutants -- all
justified as equivalent below.** Run `./scripts/mutation.sh config` for the
current score and mutant total.

Every non-equivalent mutant go-mutesting generates for this module is
killed by the test suite. `decode.go`'s `decodeText` type-assertion guard
and `numError`'s non-`*strconv.NumError` fallback both look like this
same "unreachable through decoderFor's own dispatch" shape, but neither
qualifies as equivalent: both are directly callable within the package
(`decode_test.go` is `package config`), so both are covered by direct
tests -- `TestDecodeTextRejectsNonTextUnmarshaler` and
`TestNumErrorWrapsNonNumError` -- rather than recorded here. Only
"unreachable through every path, including a direct call" justifies an
equivalence entry; "unreachable through one caller's dispatch logic" does
not.

### `decode.go`: `durationType` literal (`time.Duration(0)` -> `time.Duration(-1)` / `time.Duration(1)`)

```go
durationType = reflect.TypeOf(time.Duration(0))
```

Mutants: change the literal to `-1` or `1`.

`durationType` is used exactly once, as `t == durationType` (a
`reflect.Type` comparison). `reflect.TypeOf` returns a value's static
type, not a value that depends on the argument's numeric content --
`reflect.TypeOf(time.Duration(0))`, `reflect.TypeOf(time.Duration(1))`,
and `reflect.TypeOf(time.Duration(-1))` are all the identical
`reflect.Type` for `time.Duration`. Verified directly:

```go
reflect.TypeOf(time.Duration(0)) == reflect.TypeOf(time.Duration(1))   // true
reflect.TypeOf(time.Duration(0)) == reflect.TypeOf(time.Duration(-1))  // true
```

No input can distinguish the mutant from the original, because the two
programs compute the exact same `durationType` value. This is a true
equivalent mutant of the "constant used only for its type" shape.

### `plan.go`: `isNestedStruct`'s `t == durationType` clause (`t == durationType || ...` -> `false || ...`)

```go
func isNestedStruct(t reflect.Type) bool {
	if t == durationType || reflect.PointerTo(t).Implements(textUnmarshalerType) {
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
```

Mutant: replace the `t == durationType` operand with the constant `false`,
leaving only the `TextUnmarshaler` check ahead of the switch.

`t == durationType` is only ever true for exactly one `reflect.Type` value:
`time.Duration`. `time.Duration` is declared as `type Duration int64`, so
`reflect.TypeOf(time.Duration(0)).Kind()` is always `reflect.Int64` -- never
`reflect.Struct` or `reflect.Pointer`. Tried to construct a distinguishing
input before accepting this: the only candidate is calling
`isNestedStruct(durationType)` itself. Traced both programs on that input --

* Original: `t == durationType` is true, short-circuits the `||`, returns
  `false` immediately.
* Mutant: `false || reflect.PointerTo(durationType).Implements(textUnmarshalerType)`.
  `time.Duration` has no `UnmarshalText` method, so this is also `false`,
  and the function falls into the `switch`. `t.Kind()` is `reflect.Int64`,
  which matches neither `case reflect.Struct` nor `case reflect.Pointer`,
  so the `default: return false` branch fires.

Both programs return `false` for the only input where they could possibly
differ. Every other input leaves `t == durationType` false in both
versions, so the removed operand never changes the OR's result there
either. No input distinguishes the mutant from the original -- a true
equivalent mutant, distinct from (but the same shape as) the `decode.go`
survivor above: both stem from `durationType` being a comparison key whose
only effect is fully subsumed by a `Kind()` check that fires identically
either way for that one type.
