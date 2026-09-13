# Mutation Test Survivors

This document records mutants that survive the mutation testing threshold for each module. A module whose mutants are all killed normally has no entry -- with one exception: a module that scores 1.000 gets an entry anyway when it carries a **wiring caveat** worth recording, i.e. a place where the perfect score is true and still does not mean what a reader would take it to mean. `httpclient` below is the worked example of that exception; the **1.000 is not "everything is covered"** paragraph below, on `httpserver`, is why the exception exists.

**Mutant ids and checksums drift.** go-mutesting numbers mutants per file in
generation order, so editing a file renumbers every mutant after the edit
point. The ids below were re-confirmed against live tool output at the R0
fix wave; if they do not match what you see, re-derive them with
`go-mutesting --no-exec --do-not-remove-tmp-folder ./...` and diff the saved
mutant against its `.original`. Match entries by the **code they mutate**,
which is quoted in every section, not by the id alone.

`contract`, `httpserver`, and `examples/orders` all score 1.000 with no
survivors and no wiring caveat recorded here, so none has an entry --
`httpserver`'s caveat is written up in the **1.000 is not "everything is
covered"** paragraph instead, because it is the example the rule is built on.
`httpclient` also scores 1.000 with no survivors and *does* have an entry,
under the exception above.

**1.000 is not "everything is covered."** go-mutesting does not mutate
struct-literal field assignments, so a whole class of wiring bug is invisible
to this gate no matter what the score says. `httpserver`'s six
`Options` -> `http.Server` copies are the worked example: four of them
(`ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `MaxHeaderBytes`) could be
deleted individually with the suite still green and the score still 1.000,
found by hand at the R1 review, not by this tool. They have a field-for-field
test now. Read a perfect score as "every mutant the tool generates is killed",
which is the claim it can actually support -- and when a struct literal is
load-bearing (an `Options`, a composite config, anything wired once at
construction), write the test for it directly rather than waiting for a
survivor that will never appear.

## `config` Module

**Mutation Score: above threshold, with 5 surviving mutants -- 4 verified
equivalent and 1 a go-mutesting tooling artifact, all justified below.**
As of the R0 fix wave those are `config.go.0`, `config.go.17`,
`decode.go.25`, `decode.go.30` and `plan.go.26`. Run
`./scripts/mutation.sh config` for the current score and mutant total.

The cycle guard added to `plan.go` at the R0 fix wave produced one further
survivor on its `continue` (mutated to `break`), and it was **not**
equivalent: a `break` there drops every violation declared after the cyclic
field, which contradicts the module's "every violation at once" contract.
`TestBuildPlanKeepsWalkingAfterACycleViolation` kills it.

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
`f6bce24131d5cc0e3b26176ae7fdda1d` at the R0 fix wave; the checksum tracks
the file's content and changes whenever `config.go` does) as a survivor on
every run. Its
build failure is real (`undefined: Source`, `undefined: buildPlan`,
`undefined: Error`, and similar, all citing line numbers in the real
`config.go`), but the mutated source it saves to disk for inspection is
byte-for-byte identical to the unmutated file:

A transcript from the original investigation (the hashes move with the file;
what matters is that all three are equal):

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

**Mutation Score: 0.863636 (38/44), below the project-wide 0.85 default --
this module has its own 0.80 floor, recorded with its reason in the
`MUTATION_FLOORS` table in `scripts/mutation.sh`. The 6 surviving mutants,
identified by the mutant id `go-mutesting` itself reports (confirmed with
`go-mutesting --do-not-remove-tmp-folder ./...`, deterministic across
repeated runs), are `handler.go.6`, `handler.go.9`, `handler.go.18`,
`handler.go.19`, `logging.go.0`, and `middleware.go.7` -- all verified
equivalent below.** Run `./scripts/mutation.sh logging` for the current
score and mutant total.

History of the count, which is the part worth auditing: Task 11 added
`middleware.go` (12 new mutants, one of them -- `.6` -- equivalent). The R0
fix wave added the no-extractor eager-delegation branches to `WithAttrs` and
`WithGroup`, generating 6 more mutants, **all of which are killed** -- the
total went 37 -> 43 and the kill count 31 -> 37, so the score rose from
0.837838 to 0.860465 with the set of equivalents unchanged at 6. The final
touch-up wave added the `if sw.written` guard around the status attribute in
`Middleware` (see `middleware.go.7` below), generating one further mutant,
**killed** -- the total went 43 -> 44 and the kill count 37 -> 38, so the
score rose again to 0.863636 with the set of equivalents still unchanged at
6. That edit renumbered the one surviving `middleware.go` mutant from `.6` to
`.7`; it is the same diff (`w.status = http.StatusOK` ->
`_, _ = w.status, http.StatusOK`), re-confirmed against live tool output
(checksum `b7eb5f75e3cca84c9534ed2b4ebdad03`, unchanged). Before the Task 11
edit, `handler.go`'s mutants were also renumbered: the four handler survivors
below were `handler.go.4`, `.7`, `.14` and `.15` before it and are `.6`,
`.9`, `.18` and `.19` after it. Same four guards, same four diffs,
re-confirmed against live tool output.

`logging` is a small module (44 total mutants, versus `config`'s 228), so a handful of equivalent mutants moves its score
far more than the same count would move a larger module's. `config` reaches
0.977 with 5 equivalents diluted across 221 mutants; the same *shape* of
equivalents here, diluted across a much smaller total, caps the achievable
score at 0.80 -- the floor recorded in `scripts/mutation.sh` is a tripwire
set below the historical low, not a target. The binding rule is still
the one `config` established: every surviving mutant is either killed by a
new test or justified here with a specific, verified argument, identified
by its actual mutant id -- never "defensive programming," and never a
guess at which mutant "should" survive. That rule is satisfied. The 0.80
number is a tripwire recorded in the script, not evidence of a weaker test
suite: every *other* mutant in this file -- including two that target the
same two code regions as the survivors below (the slow-path counterparts
of the fast-path guard documented below) -- is killed. See `handler_test.go`'s
`TestWithGroupActuallyNestsSubsequentAttrs`,
`TestFastPathAddsExtractorAttrsAfterRecordOwnAttrs`,
`TestHandleClonesRecordBeforeMutatingIt`,
`TestSlowPathSkipsWithAttrsWhenExtractorContributesNothing` (kills the
slow-path pair), and `TestWithAttrsClipPreventsSiblingOpsCorruption`/
`TestWithGroupClipPreventsSiblingOpsCorruption` (target a bug `go-mutesting`
has no mutator for at all -- see the `slices.Clip` section below).

Three deliberate guards in `handler.go`/`logging.go` produce five of these
surviving mutants (the sixth, `middleware.go.7`, has its own section): two mutants
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

### `handler.go.6` and `handler.go.18`: `Handle`'s fast-path `len(h.ex) == 0` shortcut

```go
if len(h.ops) == 0 {
	if len(h.ex) == 0 {
		return h.next.Handle(ctx, r)
	}
	r = r.Clone() // required: a handler must not mutate a record it did not create
	...
```

Two distinct mutants survive on this one shortcut:
- `handler.go.6` replaces the guarded statement with a no-op:
  `return h.next.Handle(ctx, r)` -> `_, _, _ = h.next.Handle, ctx, r`
  (so the `if` still fires when `h.ex` is empty, but no longer returns
  early -- falls through to `r.Clone()` and an empty range over `h.ex`).
- `handler.go.18` replaces the condition with an always-false one:
  `len(h.ex) == 0` -> `len(h.ex) == -1` (so the shortcut never fires at
  all, for the same net effect as `.6` when `h.ex` is in fact empty).

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

### `handler.go.9` and `handler.go.19`: fast-path extractor-attrs guard

```go
r = r.Clone() // required: a handler must not mutate a record it did not create
for _, e := range h.ex {
	if attrs := e(ctx); len(attrs) > 0 {
		r.AddAttrs(attrs...)
	}
}
```

`handler.go.9` loosens `> 0` to `>= 0`; `handler.go.19` loosens it to
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

-- produces the corresponding slow-path mutants (`>= 0` and `> -1`), and
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

### `middleware.go.7`: `statusWriter.Write`'s redundant `w.status = http.StatusOK`

```go
func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.status = http.StatusOK
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}
```

Mutant: replace the assignment `w.status = http.StatusOK` with a no-op
reference to both operands (`_, _ = w.status, http.StatusOK`), leaving
`w.written = true` untouched.

**Why the line exists:** it makes the "implicit write means 200" rule
readable at its point of effect, rather than relying on a reader to trace
back to `Middleware`'s construction of the writer to see where the default
comes from.

**Why it's equivalent in behaviour:** `statusWriter.status` is initialized
to `http.StatusOK` in `Middleware` (`&statusWriter{ResponseWriter: w,
status: http.StatusOK}`) and is mutated nowhere else in the type except
this line and `WriteHeader`'s own `w.status = code`, both of which are
guarded by the identical `if !w.written` condition and both of which set
`w.written = true` in the same guarded block. So the two write sites to
`status` are mutually exclusive with respect to which one can fire first,
and whichever fires first is gated by `!w.written` being true, which is
only possible while `status` still holds its initial value (nothing else
can have changed it without also having flipped `written`). Concretely: the
body of `Write`'s guard only executes when `w.written` is still `false`,
and by the invariant above that means `w.status` has not been written by
`WriteHeader` either, so it is still exactly `http.StatusOK` -- the value
already sitting there since construction. Overwriting it with the same
value it already holds is unobservable.

Tried the direct route: reproduced the mutant by hand (see below) and ran
the full suite, including `TestMiddlewareIgnoresWriteHeaderAfterImplicitWrite`
(added specifically to probe this exact guard's `w.written = true`
companion assignment, which is a *real*, non-equivalent mutant --
`middleware.go.6` (`WriteHeader`) and `middleware.go.8` (`Write`) as of the
final touch-up wave's renumbering, re-confirmed against live tool output;
both killed by that test). With only the `w.status = http.StatusOK`
assignment removed, every test still passes:

```
$ go test -race ./...
ok  	github.com/pavelpascari/svcrt/logging	1.5s
```

This is the same "constant reassignment of an already-correct value" shape
as `logging.go.0` above, not a new failure mode: the guard's *real* job --
recording that a write happened at all, via `w.written = true` -- is
covered and enforced by `TestMiddlewareIgnoresWriteHeaderAfterImplicitWrite`;
only the redundant re-statement of the already-current default is
equivalent.

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

**Both tests must register an extractor.** Since the R0 fix wave,
`WithAttrs`/`WithGroup` delegate eagerly to `h.next` when no extractor is
present, so a no-extractor handler builds no `ops` slice at all -- there is
nothing to share and nothing to corrupt, and the test would pass while
testing plain `slog`. `traceExtractor()` contributes nothing to a bare
context, so it keeps the ops path live without changing the expected
output.

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

## `lifecycle` Module

**Mutation Score: 0.973684, 3 surviving mutants out of 114, all verified
equivalent below.** As of Task 5 those are `graph.go.5`, `graph.go.10` and
`graph.go.17` (line/id numbers per `go-mutesting --do-not-remove-tmp-folder
--verbose ./...`; re-derive as described at the top of this file if they
drift). Run `./scripts/mutation.sh lifecycle` for the current score and
mutant total.

Every other mutant go-mutesting generated for this module -- including 14
against `lifecycle.go` and 2 against `signal.go` that survived on the first
mutation run of Task 5 -- turned out to be genuine gaps, not equivalents, and
is now killed by a test:

- `Add`'s Ref-validation guard (`r.owner != l || r.i < 0 || r.i >= len(l.comps)`)
  had three weakenable sub-conditions with no test pinning any of their exact
  boundaries: `TestAddRejectsSelfReferencingRefIndex` (the `>=` boundary, i.e.
  a Ref pointing at the component currently being added),
  `TestAddRejectsNegativeRefIndex` (the `r.i < 0` arm, only reachable via a
  same-owner Ref built from inside the package), and
  `TestAddReportsEveryInvalidRefNotJustTheFirst` (`continue` vs `break`,
  proven by checking that two invalid Refs on one component each get their
  own reported error) — all in `add_internal_test.go`, `package lifecycle`.
- `defaultStopTimeout`'s literal (15s, weakenable to 14s/16s) is pinned
  directly in `defaults_internal_test.go` rather than through a multi-second
  sleep in a suite that also runs under `-race -count=10`.
- `drain`'s `DrainDelay > 0` guard had no test distinguishing `> 0` from
  `>= 0`, `> -1`, or `> 1`; `TestDrainDelayLogsOnlyWhenPositive` (`drain_test.go`)
  checks the "draining" log line's presence at `DrainDelay` of exactly zero
  (must be absent) and exactly one nanosecond (must be present), which brackets
  all three mutants.
- `startLevel`'s and `stopOne`'s `logf` calls for "starting component" and
  "stopping component" were asserted nowhere; `TestRunLogsStartingAndStoppingComponents`
  (`logging_test.go`) reads them out of a real `slog.TextHandler`.
- `stopStarted`'s `if !started[i] { continue }` becoming `break` would abandon
  every remaining sibling in a level once it hit one that never started, not
  just skip that one; `TestStopSkipsOnlyTheUnstartedSiblingNotEveryoneAfterIt`
  (`failure_test.go`) puts an unstarted component between two started ones in
  the same level and checks the later one still gets stopped.
- `stopStarted`'s `mu.Unlock()` being dropped is invisible with only one
  failing `Stop` in a run (the mutex is simply never contended again) but
  deadlocks the moment a second one does;
  `TestStopStartedJoinsErrorsFromConcurrentFailingSiblings` (`failure_test.go`)
  puts two failing `Stop`s in the same level, which hangs the whole test
  under the mutant and passes cleanly without it.
- `SignalContext`'s goroutine had two mutants: skipping the `<-ctx.Done()`
  receive (which, because `signal.NotifyContext`'s `stop` both unregisters
  the relay **and** cancels the context, would cancel the returned context
  immediately on every call, no signal needed) is killed by
  `TestSignalContextDoesNotCancelBeforeAnySignal`; dropping the inner `stop()`
  call (so a *second* signal is never restored to default handling) is killed
  by `TestSignalContextSecondSignalRestoresDefaultHandling`, which re-execs
  the test binary as a child, sends it two SIGTERMs, and checks the child
  died to the second one via the OS's default disposition rather than still
  running. Both are in `signal_test.go`.

None of the above turned out to need a source change -- Task 5's
implementation was already correct; the mutants survived only because no
test exercised the exact boundary or code path they touched.

### `graph.go`: `levels`' running-max update (`lvl[d]+1 > l`)

```go
for _, d := range c.after {
	if lvl[d]+1 > l {
		l = lvl[d] + 1
	}
}
```

Mutants: `>` → `>=` (`graph.go.5`), `+1` → `+2` on the left side only, not the
assignment (`graph.go.17`).

This loop computes `l` as the running maximum of `lvl[d]+1` over every
dependency `d`. The assignment is always `l = lvl[d] + 1` — never anything
else — so the condition's only job is deciding *whether* to perform that
exact assignment, and the two mutants only change which cases perform it:

- `>=` (`.5`): the extra case it enables is `lvl[d]+1 == l`. Assigning `l`
  the value it already equals is a no-op; the final `l` is identical either
  way, regardless of how many dependencies tie or in what order they are
  visited.
- `lvl[d]+2 > l` with the assignment left as `l = lvl[d]+1` (`.17`): the only
  extra case this enables beyond the original (`lvl[d]+1 > l`) is the single
  integer value `l == lvl[d]+1` (since `lvl[d]+2 - (lvl[d]+1) == 1`, no other
  integer `l` satisfies `lvl[d]+1 <= l < lvl[d]+2`). In that case the
  assignment sets `l` to the value it already holds — again a no-op.

Tried to distinguish both by direct construction (including via
`add_internal_test.go`'s ability to build arbitrary `component` slices and
call the unexported `levels` directly, bypassing `Add`'s validation
entirely): no sequence of dependency levels, tie counts, or visitation
orders can make either mutant's extra-triggered branch do anything other
than reassign `l` to its current value. Both are equivalent by construction,
not by inconvenience.

### `graph.go`: `levels`' `depth` seed (`depth := 0` → `depth := -1`, `graph.go.10`)

```go
if len(cs) == 0 {
	return nil
}
lvl := make([]int, len(cs))
depth := 0
for i, c := range cs {
	l := 0
	for _, d := range c.after {
		if lvl[d]+1 > l {
			l = lvl[d] + 1
		}
	}
	lvl[i] = l
	depth = max(depth, l)
}
```

The `len(cs) == 0` guard above this code means the loop always runs at least
once. Its first iteration (`i == 0`) computes `l` starting from `0` and only
ever raising it via `lvl[d]+1` for `d` in `cs[0].after` — and `lvl[d]` is
always `>= 0`, either because it is a still-zero-valued slot (`lvl` is
freshly allocated with `make`) or because it was itself set by this same
non-negative induction on an earlier iteration. So `l >= 0` on every
iteration, in particular the first, regardless of what `cs[0].after`
contains — even a self-referencing `after: []int{0}` built directly by an
internal test still resolves through the zero-valued `lvl[0]` and produces
`l >= 0`. `depth = max(depth, l)` on that first iteration therefore always
lands on the same value whether `depth` started at `0` or `-1`:
`max(0, l) == max(-1, l)` for every `l >= 0`. Every later iteration only
raises `depth` further via the same non-negative `l`, so the seed value
never has another chance to matter. Equivalent by construction: there is no
`cs` — malformed or not, constructed publicly or via direct internal access
to `component` — for which the seed is observable.

## `examples/worker` Module

**Mutation Score: above threshold, with 2 surviving mutants -- both verified
equivalent, justified below.** As of the R1 sweep (task 12) those are
`consumer.go.3` and `consumer.go.4`. `main.go` is excluded from the run
entirely (see `MUTATION_EXCLUDE` in `scripts/mutation.sh`) for the same
reason `examples/orders`' is: since the R1 fix wave it holds `func main()`
and nothing else -- config load, logger, a real OS signal handler, `Run`,
`os.Exit` -- none of which a test binary can reach. The wiring both mains
call lives in `stack.go` and is mutated like any other file. Run
`./scripts/mutation.sh examples/worker` for the current score and mutant
total.

Two other survivors found during the same sweep were **not** equivalent and
were killed by strengthening the test suite rather than documented here:
`Consumer.Start`'s "already running" early return (a missing assertion let a
second `Start` silently orphan the first goroutine) and `Consumer.Stop`'s
"not running" early return (untested on a never-`Start`ed consumer, where a
mutant that skipped the guard's `return` panicked on a nil `cancel`, and one
that skipped only the `Unlock` left the mutex held forever). See
`TestConsumerStartIsIdempotent`, `TestConsumerStopWithoutStartIsANoop`, and
the strengthened `TestConsumerStopIsIdempotent`.

### `consumer.go`: the ticker's polling interval (`50 * time.Millisecond` -> `49` / `51`)

```go
go func() {
	defer close(c.done)
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// a real consumer would handle a message here
		}
	}
}()
```

Mutants: change the literal to `49` or `51` milliseconds.

The `<-t.C` case has an empty body -- the comment says as much; this stands
in for where a real consumer would do work, and nothing here ever reads how
often the ticker fired. The only way the interval could become observable is
through `Stop`'s shutdown latency, and it isn't: `Stop` cancels `ctx`, and
the loop's `case <-ctx.Done(): return` races the *next* `select` evaluation,
not the next tick -- the two cases are independent, so cancellation is never
queued behind whichever period is compiled in. `TestConsumerStopsProcessing`
and `TestConsumerStopRespectsContextDeadline` both call `Stop` immediately
after `Start` and neither result depends on the constant's value. No input --
including a `Stop` deadline deliberately placed close to the tick period --
distinguishes `49ms`, `50ms`, and `51ms`, because no code path branches on
elapsed ticks or measures the interval. Equivalent by construction: this
literal has no observer, not merely one the current tests happen to miss.

## `httpclient` Module

**Mutation Score: 1.000, no survivors** -- an entry under the preamble's
exception, because the caveat is worth recording. As with the preamble's
`httpserver` worked example, that score does not mean every wiring is verified
by mutation. `go-mutesting` does not mutate struct-literal field assignments,
so `New`'s `Options` -> `http.Transport` copy -- `TLSHandshakeTimeout`,
`ResponseHeaderTimeout`, `IdleConnTimeout`, `ExpectContinueTimeout`,
`MaxIdleConns`, `MaxIdleConnsPerHost`, `TLSClientConfig`, and
`Options.Timeout` -> `Client.Timeout` -- is invisible to this gate no matter
what the score says: a mutant that swapped,
say, `IdleConnTimeout` and `ExpectContinueTimeout` on both sides of the
assignment would compile and leave the score at 1.000. What actually covers
that wiring is `TestEveryOptionLandsOnItsOwnDestination`
(`httpclient/client_test.go`), which assigns every field a distinct value and
asserts each lands on its own destination rather than merely a non-zero one.
`DialTimeout` is not in that table, because it feeds `net.Dialer.Timeout`
inside a closure rather than a transport field. It used to rest on code
inspection alone, and the R2 review confirmed the cost by running the mutants:
zeroing the dial timeout, and swapping it with the keep-alive interval, both
survived the full suite. `New` now builds that dialer through `newDialer`, and
`TestDialerCarriesTheDialTimeoutAndKeepAlive`
(`httpclient/client_internal_test.go`) reads both values back with a distinct
value per field, so neither mutant survives any more. Read a 1.000 here the
same way the preamble reads `httpserver`'s: "every mutant the tool generates is
killed", not "every wiring is verified" -- the latter is this table's job, not
go-mutesting's.
