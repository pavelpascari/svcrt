# Mutation Test Survivors

This document records mutants that survive the mutation testing threshold for each module. Modules with perfect coverage (all mutants killed) have no entry.

## `config` Module

**Mutation Score: above threshold, with 2 surviving mutants -- all
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
