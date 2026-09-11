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

## `logging` Module

**Mutation Score: 0.80 (20/25), below the project-wide 0.85 default -- this
module has its own 0.80 floor in `scripts/mutation.sh` (see the `case`
statement there). The 5 surviving mutants, identified by the mutant id
`go-mutesting` itself reports (confirmed with
`go-mutesting --do-not-remove-tmp-folder ./...`, deterministic across
repeated runs), are `handler.go.4`, `handler.go.7`, `handler.go.14`,
`handler.go.15`, and `logging.go.0` -- all verified equivalent below.**
Run `./scripts/mutation.sh logging` for the current score and mutant total.

`logging` is a small module (25 total mutants, versus `config`'s 221), so a
handful of equivalent mutants moves its score far more than the same count
would move a larger module's. `config` reaches 0.977 with 5 equivalents
diluted across 221 mutants; the same *shape* of equivalents here, diluted
across only 25, caps the achievable score at 0.80. The binding rule is still
the one `config` established: every surviving mutant is either killed by a
new test or justified here with a specific, verified argument, identified
by its actual mutant id -- never "defensive programming," and never a
guess at which mutant "should" survive. That rule is satisfied. The 0.80
number is a tripwire recorded in the script, not evidence of a weaker test
suite: every *other* mutant in this file -- including two that target the
same two code regions as the survivors below (`handler.go.8` and
`handler.go.16`, the slow-path counterpart of the fast-path guard
documented below) -- is killed. See `handler_test.go`'s
`TestWithGroupActuallyNestsSubsequentAttrs`,
`TestFastPathAddsExtractorAttrsAfterRecordOwnAttrs`,
`TestHandleClonesRecordBeforeMutatingIt`,
`TestSlowPathSkipsWithAttrsWhenExtractorContributesNothing` (kills
`handler.go.8`/`.16`), and `TestWithAttrsClipPreventsSiblingOpsCorruption`/
`TestWithGroupClipPreventsSiblingOpsCorruption` (target a bug `go-mutesting`
has no mutator for at all -- see the `slices.Clip` section below).

Three deliberate guards produce these five surviving mutants: two mutants
each target the same `len(h.ex) == 0` shortcut and the same fast-path
`len(attrs) > 0` check (one mutating the condition, one the guarded
statement, or the operator), and one targets `New`'s default-level
assignment. Two of the three guards are kept specifically because
mutation testing cannot see the dimension they exist for: **mutation
testing measures behaviour, not cost, and these guards exist purely to
bound allocations on a per-request path** (`logging`'s handler runs on
every HTTP request). Removing them would not fix a test gap -- it would
delete real work the guards do, to satisfy a proxy metric, at the expense
of the actual goal (bounded allocations) that metric is supposed to
serve. **Do not delete these guards on the strength of this document's own
equivalence proofs** -- the proofs establish that *behaviour* is
unaffected, not that the guards are useless.

### `handler.go.4` and `handler.go.14`: `Handle`'s fast-path `len(h.ex) == 0` shortcut

```go
if len(h.ops) == 0 {
	if len(h.ex) == 0 {
		return h.next.Handle(ctx, r)
	}
	r = r.Clone() // required: a handler must not mutate a record it did not create
	...
```

Two distinct mutants survive on this one shortcut:
- `handler.go.4` replaces the guarded statement with a no-op:
  `return h.next.Handle(ctx, r)` -> `_, _, _ = h.next.Handle, ctx, r`
  (so the `if` still fires when `h.ex` is empty, but no longer returns
  early -- falls through to `r.Clone()` and an empty range over `h.ex`).
- `handler.go.14` replaces the condition with an always-false one:
  `len(h.ex) == 0` -> `len(h.ex) == -1` (so the shortcut never fires at
  all, for the same net effect as `.4` when `h.ex` is in fact empty).

**Why the guard exists:** `logging.NewHandler` can be constructed with zero
extractors (a plain enrichment-free wrap), and this is the hot path for
every request when that's the case. Without the guard, every single record
pays for a `Record.Clone()` call it will never use, on every request.

**Why both are equivalent in behaviour:** `r` is a value parameter local to
`Handle`; nothing after the fast-path return ever uses it again once it's
handed to `h.next.Handle(ctx, r)`. `Clone()`'s only effect is
`r.back = slices.Clip(r.back)`, which rewrites the *local copy's* slice
header (length/cap), not the underlying array, and not any other copy's
header. Since `len(h.ex) == 0` means the subsequent `for _, e := range h.ex`
loop never executes regardless of either mutation, no `AddAttrs` call ever
follows the (skipped-or-not) `Clone()`. A slice-header rewrite with no
following mutation and no further use of the value is unobservable through
any output-based test, any JSON diff, or any spy on `next` (both branches
call `next.Handle` exactly once, with content that is byte-identical).
Verified directly for both mutants: reproducing each edit by hand and
running the full suite (including `TestHandleClonesRecordBeforeMutatingIt`,
built specifically to detect a missing/extra clone via
`slog.Record.AddAttrs`'s own corruption self-check -- see below) leaves
every test green either way.

### `handler.go.7` and `handler.go.15`: fast-path extractor-attrs guard

```go
r = r.Clone() // required: a handler must not mutate a record it did not create
for _, e := range h.ex {
	if attrs := e(ctx); len(attrs) > 0 {
		r.AddAttrs(attrs...)
	}
}
```

`handler.go.7` loosens `> 0` to `>= 0`; `handler.go.15` loosens it to
`> -1`. Both make the guard always true, so `r.AddAttrs(attrs...)` runs
even when an extractor contributes nothing (`attrs` nil or empty -- the
documented, expected case for a context with no correlation data,
exercised by `TestExtractorContributesNothingWhenContextIsBare`).

**Why the guard exists:** an extractor runs on every record ("must be
cheap"), and contributing nothing is the *common* case for background work
with no request context. The guard skips a call into `Record.AddAttrs`
that would otherwise run, unconditionally, on every enriched record.

**Why both are equivalent in behaviour:** by this point `r` has already
been cloned, so `r.back`'s capacity equals its length exactly
(`slices.Clip`). Reading `log/slog`'s own `Record.AddAttrs` source
confirms a zero-length call is a strict no-op regardless: its fill loop is
bounded by `len(attrs)` (0 iterations), its "was this copy mutated
elsewhere" self-check requires `cap(r.back) > len(r.back)` (false, just
after `Clip`), and `slices.Grow(r.back, 0)` performs no allocation. There is
no `attrs` value (nil or a genuine empty slice both have `len == 0`) for
which calling `AddAttrs` differs observably from not calling it at all, on
a freshly cloned record. Tried the direct route first: since `Record` is a
value type with no interface to spy on the way `slog.Handler` has, the only
way to distinguish "was AddAttrs called" from "was it skipped" is to
observe `r`'s own state afterward, and that state is identical either way
by construction of `AddAttrs` itself, not by coincidence.

The slow-path counterpart of this same guard --

```go
n := h.next
for _, e := range h.ex {
	if attrs := e(ctx); len(attrs) > 0 {
		n = n.WithAttrs(attrs)
	}
}
```

-- produces `handler.go.8` (`>= 0`) and `handler.go.16` (`> -1`), and
**both are killed**, not equivalent: `log/slog`'s own
`commonHandler.withAttrs` treats a zero-length call as a no-op for the
*standard* handler, but `TestSlowPathSkipsWithAttrsWhenExtractorContributesNothing`
wraps a custom spy `slog.Handler` that counts `WithAttrs` calls regardless
of arguments, to check whether the *call itself* happens rather than
whether the standard handler shrugs it off. That test fails the moment
either guard is loosened (confirmed by reproducing each mutation by hand:
`next.WithAttrs called 1 times for an extractor that contributed
nothing`). This is the fast/slow-path asymmetry to watch for: the same
guard shape is equivalent on one path and killed on the other, because
`Record.AddAttrs` and `slog.Handler.WithAttrs` differ in whether a
zero-length call is inert *in a way this package's own tests can observe*.

### `logging.go.0`: `New`'s default-level assignment

```go
level := opts.Level
if level == nil {
	level = slog.LevelInfo
}
base := slog.NewJSONHandler(w, &slog.HandlerOptions{
	Level: level,
	...
```

Mutant: delete the assignment inside the `if`, so `level` stays `nil` and is
passed to `slog.HandlerOptions.Level` as-is.

**Why the line exists:** it exists to make `Options.Level`'s doc comment
("Nil means slog.LevelInfo") true by *this package's own code*, not by an
inherited default from a dependency. That is deliberate API-surface
explicitness, independent of whether `slog` happens to default the same
way today.

**Why it's equivalent in behaviour today:** `slog.HandlerOptions.Level`'s
own doc says "If Level is nil, the handler assumes LevelInfo" -- verified
directly (not just by reading the doc) by constructing two
`slog.NewJSONHandler`s, one with `Level: nil` and one with
`Level: slog.LevelInfo`, and comparing `Enabled()` across Debug/Info/
Warn/Error: identical for both. So today, `level == nil` reaching
`slog.NewJSONHandler` unchanged behaves exactly like explicitly setting
`slog.LevelInfo`. This is an equivalence borrowed from a dependency's
current documented behaviour, not a property of this package's own logic
-- if `log/slog` ever changed its own default, this "equivalent" mutant
would stop being equivalent. The line stays for that reason as much as for
the doc-comment one.

### `slices.Clip` in `WithAttrs`/`WithGroup`: correct, but no mutator can see it

Not a survivor -- `go-mutesting` has no mutator for "delete this stdlib
call," so it never generates a mutant for `slices.Clip(h.ops)` at all. That
means the gate is silent on this line by construction, not because it's
safe to remove. Worth recording here so the next reader doesn't mistake
"no mutant" for "no risk."

```go
ops: append(slices.Clip(h.ops), op{attrs: attrs}),   // WithAttrs
ops: append(slices.Clip(h.ops), op{group: name}),    // WithGroup
```

Stripping `slices.Clip` from either call and running the full suite before
adding new tests: **all tests still passed.** `TestWithAttrsDoesNotMutateReceiver`
only chains one level deep and never branches two handlers off the same
capacity-loaded parent, so it can't see the bug.

The bug: chaining `WithAttrs`/`WithGroup` enough times (empirically, Go's
own append growth leaves spare capacity well before 18 single-op appends --
confirmed directly: a 3-element `[]op` already has `cap` 4) leaves a
handler's `ops` slice with `cap > len`. Branching two handlers off that
same parent with `append(h.ops, ...)` (no `Clip`) makes both appends target
the *same* index in the shared backing array; whichever branch is
constructed second silently overwrites the first branch's op in memory
that the first branch's own (already-returned) `ops` slice still points
into -- so when the first branch is finally used to `Handle` a record, it
reads and applies the *second* branch's op instead of its own.
`TestWithAttrsClipPreventsSiblingOpsCorruption` and
`TestWithGroupClipPreventsSiblingOpsCorruption` reproduce exactly this:
grow a chain of 18 `WithAttrs`/`WithGroup` calls, branch two children with
different attrs/group names off that same parent, and check each renders
its own value.

One non-obvious wrinkle when testing `WithGroup` specifically: don't chain
a further `WithAttrs`/`WithGroup` call onto each branch to give it
something to render. That subsequent call's own (correctly clipped) append
copies the vulnerable op out of the shared array before the sibling gets a
chance to corrupt it, which silently masks the exact bug under test.
Instead, give each branch's own `Record` an attr directly (`r.AddAttrs`) at
`Handle` time -- a group nests a record's own attrs the same way it nests
`WithAttrs`-attached ones, without needing another tracked `op`.

Verified both directions for both methods, by manually removing
`slices.Clip` from each call site in turn and reverting after:

```
$ go test -race -run TestWithAttrsClipPreventsSiblingOpsCorruption -v ./...   # Clip removed from WithAttrs
    handler_test.go:398: left branch = right, want left (sibling ops slice was overwritten)
--- FAIL: TestWithAttrsClipPreventsSiblingOpsCorruption (0.00s)

$ go test -race -run TestWithAttrsClipPreventsSiblingOpsCorruption -v ./...   # Clip restored
--- PASS: TestWithAttrsClipPreventsSiblingOpsCorruption (0.00s)

$ go test -race -run TestWithGroupClipPreventsSiblingOpsCorruption -v ./...   # Clip removed from WithGroup
    handler_test.go:451: left line missing its own "left" group or contains "right" (sibling ops slice was overwritten): {...,"g17":{"right":{"k":"v"}}}
--- FAIL: TestWithGroupClipPreventsSiblingOpsCorruption (0.00s)

$ go test -race -run TestWithGroupClipPreventsSiblingOpsCorruption -v ./...   # Clip restored
--- PASS: TestWithGroupClipPreventsSiblingOpsCorruption (0.00s)
```

### The `r.Clone()` corruption-oracle technique (used to kill, not survive, a mutant)

Worth recording here because it is non-obvious and the next person to touch
`handler.go` will need the same trick: `TestHandleClonesRecordBeforeMutatingIt`
proves the fast path's `r = r.Clone()` (in the branch with a non-empty
`h.ex`) is load-bearing, without relying on a data race or on inspecting any
unexported field. `slog.Record.AddAttrs` contains its own internal defense
against exactly the bug this line prevents:

```go
// Check if a copy was modified by slicing past the end
// and seeing if the Attr there is non-zero.
if cap(r.back) > len(r.back) {
	end := r.back[:len(r.back)+1][len(r.back)]
	if !end.isEmpty() {
		r.back = slices.Clip(r.back)
		r.back = append(r.back, String("!BUG", "AddAttrs unsafely called on copy of Record made without using Record.Clone"))
	}
}
```

So: build a `Record` whose internal `back` overflow slice has spare
capacity (empirically, fill the 5-slot front array, then add attrs one at a
time -- by the third individual back-append the slice consistently has
`cap > len`; bulk `AddAttrs(all-at-once)` does not reproduce this because
`slices.Grow` sizes an all-at-once call exactly), take a `sibling := base`
copy while that capacity is still shared, run `base` through the handler
under test, then call `sibling.AddAttrs(...)` and dump it through a plain
`slog.JSONHandler`. If the handler under test mutated `base`'s shared array
without cloning, `sibling`'s own next `AddAttrs` call trips `log/slog`'s
own guard and the dump contains `"!BUG":"AddAttrs unsafely called on copy
of Record made without using Record.Clone"`. This is deterministic --
no goroutines, no timing, no reliance on a specific Go slice-growth
implementation detail beyond the one empirically pinned above -- and was
confirmed both ways: it reproduces `!BUG` when `r.Clone()` is removed, and
stays clean when it is present.
