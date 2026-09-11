# Mutation Test Survivors

This document records mutants that survive the mutation testing threshold for each module. Modules with perfect coverage (all mutants killed) have no entry.

## `config` Module

**Mutation Score: above threshold, with 5 surviving mutants -- 4 verified
equivalent and 1 a go-mutesting tooling artifact, both justified below.**
Run `./scripts/mutation.sh config` for the current score and mutant total.

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

### `plan.go`: `walk`'s collapsed `env:"-"` guard (`hasEnv && envTag == "-"` -> `true && envTag == "-"`)

```go
envTag, hasEnv := f.Tag.Lookup("env")
if hasEnv && envTag == "-" {
	continue
}
```

Mutant: replace the `hasEnv` operand with the constant `true`, leaving only
`envTag == "-"`.

`reflect.StructTag.Lookup` returns `("", false)` when the key is absent --
`envTag` can only ever be `"-"` when `hasEnv` is also `true`; there is no
way to get `Lookup("env")` to return `("-", false)`. Tried the direct route
before accepting this: `walk` is unexported but reachable from
`plan_test.go` (`package config`) via `buildPlan`, and the package already
runs an untagged exported field directly through this exact line --
`TestBuildPlanRejectsUntaggedExportedField`'s `Oops string` field has
`hasEnv == false`, `envTag == ""`. Both the original
(`hasEnv && envTag == "-"` is `false`) and the mutant
(`true && envTag == "-"` is `false`, since `"" != "-"`) evaluate identically
on that direct call, because `hasEnv` and `envTag == "-"` are never
independent -- the guard's own zero-value semantics make `hasEnv` fully
redundant here. No input, including this existing direct call, distinguishes
the mutant from the original.

### `config.go`: `Load`'s block-presence inner loop (`break` -> `continue`)

```go
for i, b := range p.bindings {
	if underBlock(b.index, blk.index) && res[i].found {
		present = true
		break
	}
}
```

Mutant: replace `break` with `continue`.

This loop's only effect is setting the local `present` flag; it has no
other side effect and nothing reads `i` or `b` after the assignment. Once
`present` is `true`, every subsequent iteration (mutant) either leaves it
`true` (matching bindings) or does nothing to it (non-matching bindings) --
the loop's post-state is identical to stopping immediately (original)
either way. Checked whether a direct call could distinguish them: this
code lives in `Load`, which is called only through the public `config`
API, but even a hypothetical call with every binding under the block
"found" produces the same final `present = true` under both versions, just
after a different number of harmless iterations. No input, including
adversarial block layouts with many bindings, distinguishes the two: this
is a true equivalent mutant, not a "break exits earlier so it must be
observable" false alarm -- the iteration count itself is not observable
from any test.

### `config.go.0`: go-mutesting tooling artifact, not a real mutation

go-mutesting reports one `config.go` mutant (index `0`, checksum
`bb1b403603c5500d08629fdf27071865`) as a survivor on every run. Its
build failure is real (`undefined: Source`, `undefined: buildPlan`,
`undefined: Error`, and similar, all citing line numbers in the real
`config.go`), but the mutated source it saves to disk for inspection is
byte-for-byte identical to the unmutated file:

```
$ md5 config.go go-mutesting-<run>/config.go.0 go-mutesting-<run>/config.go.original
MD5 (config.go) = f613f2127d236b247cabe9b250a23322
MD5 (go-mutesting-<run>/config.go.0) = f613f2127d236b247cabe9b250a23322
MD5 (go-mutesting-<run>/config.go.original) = f613f2127d236b247cabe9b250a23322
```

Reproduced across two independent full runs (`--do-not-remove-tmp-folder`),
both times with the identical checksum and identical byte-for-byte dump.
There is no `--- Original` / `+++ New` diff printed for this mutant either,
unlike every other reported mutation. Since the artifact go-mutesting saved
contains no code change at all, there is nothing for any test to
distinguish -- this is not a claim about test coverage, equivalent or
otherwise; it is a reporting anomaly in the tool itself (most likely a
file-swap/restore ordering issue around whichever mutator targets the
first construct in the file). Not something a test can address.
