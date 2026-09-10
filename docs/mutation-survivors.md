# Mutation Test Survivors

This document records mutants that survive the mutation testing threshold for each module. Modules with perfect coverage (all mutants killed) have no entry.

## `config` Module

**Mutation Score: above threshold, with 3 surviving mutants -- all
justified as equivalent below.** Run `./scripts/mutation.sh config` for the
current score and mutant total.

Every non-equivalent mutant go-mutesting generates for this module is
killed by the test suite. The survivors below are re-checked whenever
`config/decode.go` changes, since a future edit could make one of them
reachable (e.g. `decoderFor` gaining a second, less careful caller).

### `decode.go`: `decodeText`'s `!ok` type-assertion-failure branch

```go
u, ok := dst.Addr().Interface().(encoding.TextUnmarshaler)
if !ok {
	return fmt.Errorf("internal: %s is not a TextUnmarshaler", dst.Type())
}
```

Mutant: replaces the `return` with a no-op, so the branch body does nothing.

`decodeText` is only ever installed as the decoder for a type `t` after
`decoderFor` has already checked `reflect.PointerTo(t).Implements(textUnmarshalerType)`
(decode.go, the check just above the kind switch). `dst` passed to a
built decoder is always addressable and of that same type `t` (see
`decodeInto` in decode_test.go: `dec(raw, reflect.ValueOf(&v).Elem())`), so
`dst.Addr()` is always a `*t`, which is guaranteed to implement
`TextUnmarshaler`. The assertion therefore cannot fail through any call
path reachable from `decoderFor`, so no input distinguishes the mutant
from the original -- the branch body is dead by construction, not by
missing test coverage. Confirmed by attempting to construct a
distinguishing input: none exists, because `ok` is invariantly `true`.

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
