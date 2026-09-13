# svcrt conventions

Decisions that R0 made by doing rather than by writing down. Each is recorded
here because the next module has to make the same choice, and guessing would
produce a second dialect.

These are conventions, not laws. Departing from one is fine; departing from
one silently is not — amend this file in the same change.

---

## 1. Functional options vs. an `Options` struct

Both shapes are in the tree:

| Package | Shape |
|---|---|
| `config` | `type Option func(*options)`, `WithSource(...)`, `WithPrefix(...)` |
| `logging` | `type Options struct { Level; AddSource; ReplaceAttr }` |

That is not an inconsistency; the two cases are different, and the rule that
separates them is:

- **Configuring a *value*, where the knobs mirror a standard-library struct —
  use a struct, and mirror it field for field.** `logging.Options` is
  `slog.HandlerOptions` with the same three fields under the same names. A
  reader who knows one knows the other, and its zero value is a meaningful
  default. Wrapping those three fields in `WithLevel`/`WithAddSource`/
  `WithReplaceAttr` would make a caller learn a second dialect for knobs they
  already know, and would buy nothing.

- **Configuring a *call*, where the common case passes nothing — use
  functional options.** Production calls `config.Load[AppConfig]()` with no
  arguments at all; `WithSource` exists mainly so a test can supply a map
  instead of the process environment. Functional options keep the common call
  empty and let a caller add exactly one thing.

**When neither clearly applies, prefer the struct.** It is inspectable,
zero-valued, and costs no closure per setting. Reach for functional options
when an option must carry behaviour, when options are expected to be added by
modules that do not exist yet, or when the zero value of some field would be a
dangerous default that a struct literal lets a caller omit by accident.

## 2. `Middleware` means four things, on purpose — never a fifth

R0 declared two spellings of `Middleware` and warned a third would be one too
many. R1 added the third anyway, deliberately, once `httpserver` needed a
name for the shape the other two were already describing in prose. R2 added a
fourth, the client-side mirror of that third, once `httpclient` needed the
same shape on `http.RoundTripper` instead of `http.Handler`. R3 needed that
exact shape again, for `resilience`'s retry and timeout middleware, and did
not add a fifth — see below for why declaring one would have been a mistake,
not just a style preference. All four stand:

- `contract.Middleware[Req, Res]` is a **generic type**: the call-level seam,
  `func(Handler[Req, Res]) Handler[Req, Res]`. It exists so hand-written and
  generated code spell the same thing the same way.
- `logging.Middleware(l *slog.Logger)` is a **constructor function** returning
  a transport middleware — `func(http.Handler) http.Handler`.
- `httpserver.Middleware` is the plain **transport-level type** those
  constructors return: `func(http.Handler) http.Handler`. It has no logic of
  its own; it names the shape `logging.Middleware` (and every other
  transport-level constructor) builds, so `Chain` and its callers have
  something concrete to write down.
- `httpclient.Middleware` is the client-side mirror of
  `httpserver.Middleware`: `func(http.RoundTripper) http.RoundTripper`. It
  shares `httpserver.Middleware`'s ordering (in `Chain`, the first argument
  ends up outermost) and its empty-slice identity behaviour (`Chain()` with no
  arguments returns a middleware that changes nothing), so the two `Chain`
  functions read the same way even though one wraps a `Handler` and the other
  a `RoundTripper`.

The split is along the transport/call boundary, not along taste:

- **Call-level middleware** — anything that needs the decoded, typed request —
  uses `contract.Middleware[Req, Res]`. A new module must not declare its own
  generic middleware type; it imports `contract`, which is what `contract` is
  for.
- **Transport-level middleware** — anything that needs only
  `http.ResponseWriter` and `*http.Request` (logging, tracing, request IDs) —
  uses `httpserver.Middleware`'s shape and does not redeclare the type. Export
  a **constructor** returning `httpserver.Middleware` (equivalently,
  `func(http.Handler) http.Handler`). Name the constructor `Middleware` when
  the package has exactly one, as `logging` does, and name it for its job
  (`RecoverMiddleware`, `RequestIDMiddleware`) when it has several.
- **Client-side transport middleware** — anything that wraps outgoing
  requests instead of incoming ones — uses `httpclient.Middleware`'s shape,
  `func(http.RoundTripper) http.RoundTripper`, for the same reason
  `httpserver.Middleware` exists on the server side: something concrete for
  `httpclient.Chain` and its callers to write down.

So: one generic type for the call level, one plain type for the server-side
transport level, one plain type for the client-side transport level, and as
many constructors returning the relevant plain type as there are
transport-level concerns. A new module that owns, or may import, the package
holding the type it targets never declares a second call-level, server-side,
or client-side `Middleware` type — it names its constructor and returns the
existing one.

A module that targets someone else's type *without* importing that type's
package is a different case, not a fifth spelling: it must return the bare,
unnamed func type instead of a same-shaped type of its own. `resilience` is
the worked example. `Retry` and `Timeout` wrap `http.RoundTripper` —
exactly `httpclient.Middleware`'s shape — but `resilience` depends on
nothing outside the standard library, so it cannot import `httpclient` to
name its return type `httpclient.Middleware`. Declaring
`type Middleware func(http.RoundTripper) http.RoundTripper` in `resilience`
instead, for tidiness, would look harmless and would not compile through:
`httpclient.Chain(resilience.Retry(p))` requires the argument to be
assignable to `httpclient.Middleware`, and Go does not assign one named type
to another merely because their underlying types match — two named types
with identical underlying types are still different types, full stop. An
*unnamed* func type carries no such restriction: it is assignable to any
named type sharing its underlying type, which is exactly the property
`resilience` needs and a same-shaped named type would throw away. So `Retry`
and `Timeout` return the bare `func(http.RoundTripper) http.RoundTripper`,
never a named type of their own — and that is the general rule for this
case: a module shipping middleware for a type it does not own returns the
bare func type, precisely so neither module has to import the other.

## 3. Dead code: when to delete a clause and when to keep and test it

The rule, arrived at three times during R0 and settled at the third:

> **Delete a clause that is inert through every path including a direct call;
> keep and test one whose difference some caller can observe.**

Worked examples from R0:

- **Deleted.** A second `strings.TrimSpace(key)` immediately after an outer
  one. `TrimSpace` is idempotent, so no input distinguishes the two programs
  at the statement's own level. Deleted rather than recorded as an equivalent
  mutant.
- **Deleted.** A `t == durationType ||` operand in `isNestedStruct`.
  `time.Duration` is an `int64`, so both programs return `false` on the only
  candidate input. The "it mirrors `decoderFor` for readability" defence is
  about intent, not correctness, and does not survive this rule.
- **Kept and tested.** `decodeText`'s `!ok` guard is unreachable *through
  `decoderFor`* but perfectly reachable at the function's own level, which is
  where an internal white-box test operates. Deleting it would turn a future
  wiring bug into a raw panic inside a package whose reason for existing is
  structured errors instead of crashes.
- **Kept and tested.** `NewService(nil)`'s nil-map substitution. Invisible
  through `GetOrder` (reads from a nil map are harmless) but observable by a
  direct call followed by a write.

The corollary matters as much as the rule: **a mutation-survivors file that
accumulates "this code does nothing" entries makes dead code permanent**,
which inverts the purpose of the gate. An equivalence proof is a reason to
look harder, not a reason to stop.

And when a guard is kept for a reason mutation testing is structurally blind
to — cost, not behaviour — record *why the guard exists* next to the
equivalence proof. Otherwise the next reader deletes it citing that very
proof.

## 4. Supplied tests are a floor, never a target

A test suite handed down in a plan, a brief, or a task description is the
minimum, not the goal. R0 measured this twice:

- The plan's own tests for `config`'s plan walker reached 98.1% coverage and
  0.9298 mutation score. The implementer's additions took the same code to
  100% and 0.9838 over an identical 185 mutants — and caught six real bugs the
  supplied tests missed, including two `time.Time` ordering bugs and six
  `continue`→`break` truncation mutants.
- The brief for `examples/orders` left both `Error()` methods unexercised
  (83.3%), and the assertion that eventually covered them had to pin the whole
  string: a `Contains` check on either number passes with the arguments
  swapped.

So: run coverage *and* mutation, treat every survivor as killable until proven
otherwise, and expect to write tests nobody asked for. Conversely, a test that
exists only to touch a line is a defect even when it raises the number.

## 5. The `-count` policy for concurrent modules

A module with goroutines, timers, or shared state runs `-race -count=10` in
CI, not `-count=1`. Every test that can block carries a hard timeout and
fails rather than hanging, so a run that would otherwise wedge forever fails
loudly and quickly instead.

Rationale worth restating because it is not obvious from the gate list alone:
coverage and mutation testing are both blind to concurrency. R1 found a real
data race behind 100% coverage and a 0.97 mutation score in one such module —
every existing test gave the racing call a 20ms head start, so the race never
fired under either gate. `-count=10` (with `-race`) is the check that would
have caught it; a single green run says almost nothing about a module whose
bugs are "hangs one run in fifty," not "returns the wrong value." `lifecycle`,
`httpserver` and `resilience` run this way; the other library modules stay at
`-count=1`.

The list lives in `scripts/lib.sh` (`COUNT_MODULES`) and is **asserted against
the modules on disk, in both directions**. It has to be hand-kept -- it is a
judgement about which modules are concurrent, not an inventory -- and a
hand-kept list of module names is exactly the failure mode the derived
`MODULES` list exists to avoid: a rename or a typo leaves the entry matching
nothing, the module silently drops to `-count=1`, and CI still prints OK while
this section still claims the gate applies. A gate that can quietly stop
applying is worse than no gate, because the documentation keeps vouching for
it.

That argument was made here at R2 and a guard was written for it -- and then
R3 shipped the other half of the same failure anyway. `resilience` has a
timer and a retry loop on the request path, the spec named it in bold as
needing this gate, and it ran a whole milestone at `-count=1` because nobody
added it to the list. The forward guard could not see it: a name matching
**nothing** is caught, a module missing from the list is not. So `lib.sh` now
also asserts the **inverse** -- every module NOT in `COUNT_MODULES` is grepped
for the constructs the gate exists for (`time.NewTimer`, `time.After`, a
goroutine launch, `sync.`, `atomic.`) and must either join the list or carry a
`COUNT_EXEMPT` entry saying, in a sentence, why it needs no repeat runs. It is
a heuristic, and it errs toward asking: a false alarm costs one line of
justification, and the miss it replaces cost a milestone.

`scripts/release.sh` applies the same policy, not a weaker one. Tagging is the
point after which a version is permanent, so it is the last place the policy
can still be enforced; both scripts source the one definition rather than
keeping a copy each.

## 6. Probe paths are fixed

`/healthz` is liveness, `/readyz` is readiness, `/startupz` is startup. These
three paths do not vary by module or by exemplar — they are part of a
deployment's contract (the orchestrator that polls them is configured against
the path, not the binary), so a new service does not get to rename them or
add a fourth.

## 7. Never `import _ "net/http/pprof"` from a library

`net/http/pprof`'s `init()` registers its handlers on `http.DefaultServeMux`
as a side effect of being imported — by anything, transitively, whether or
not the importer wanted pprof exposed. A library that imports it hands every
consumer an undocumented, unauthenticated profiling endpoint the moment they
import the library at all.

Use `svcrt/httpserver/pprof` instead: the registration side effect lives in a
package a user imports deliberately, by name, because they want it. `httpserver`
itself does not import `net/http/pprof`.

## 8. An exemplar's `main()` calls the same wiring function its tests do

R1 learned this the expensive way: `examples/orders` originally had its
acceptance tests build a parallel stack instead of calling into `main.go`.
Breaking `main.go` four different ways left every test green, because `go
test` never calls `main()` — the tests were exercising a stack `main()` didn't
actually build.

The rule: extract the wiring into a function (`buildXStack` or similar) that
both `main()` and the tests call. Leave in `main` only what fails loudly — a
crash, a non-zero exit — never a wiring decision that could fail silently by
just doing the wrong thing while still returning 0.

## 9. Mutation testing runs in a worktree, never in the repo

`go-mutesting` rewrites source files in place and restores them only on a
clean exit, so it is not safely interruptible. Killed mid-run — Ctrl-C, a
timeout, a CI step that exceeds its budget — it leaves tracked files mutated
on disk.

This repo has hit that five times. Once it left `config/plan.go` and
`config/source.go` mutated with the whole suite passing against the corrupted
source, one `git add` from being committed. Once it mutated
`lifecycle/lifecycle.go` while a reviewer was reading that same file, where an
injected fault reads exactly like a real concurrency defect.

Each incident was answered with a better detector — a `*.tmp` sweep, a `git
diff` check against HEAD, a dirty-tree guard. Every detector worked. The
incidents kept happening, because detection is not prevention: all of it
relied on nobody running two things at once, and `scripts/mutation.sh` is
exactly the script you leave running while you do something else.

The rule: `scripts/mutation.sh` mutates a throwaway `git worktree` at HEAD.
The repo is unreachable by construction, a killed run is recovered with `git
worktree prune` instead of by restoring source, and two runs — or a run and a
reviewer — no longer collide. If you find yourself adding another check that
the working tree survived a mutation run, the isolation has regressed; fix
that instead.

Two consequences follow, and both are deliberate. Uncommitted changes are not
mutated, because a worktree is checked out at a commit — the script warns
when the tree is dirty rather than reporting a score that silently omits your
edit. And the run uses `go.work` instead of `GOWORK=off`, which is what
retired the synthesized-module machinery the exemplars used to need: with the
workspace in scope they resolve their unpublished siblings natively, so no
`replace` directives have to be injected into a committed `go.mod`. Proving
each module stands alone is `ci.sh`'s job, and it still does it.

## 10. Commit before you mutate; learnings land separately

Git is a working tool here, not only a publishing step. Commit the change you
want to improve, *then* run the mutation gate. Whatever the run teaches — a
test that kills a survivor, an equivalent mutant with its argument written
down — goes in as its own follow-up commit.

Intermediate commits on a working branch are fine. A branch is a workspace;
the PR boundary is where the story gets told, and `git rebase -i` exists for
the gap between the two.

Three independent reasons land on the same rule.

**The gate cannot see uncommitted work.** §9's worktree is checked out at HEAD,
so an edit you have not committed is not mutated. The script warns and lists
the dirty files rather than reporting a score that quietly omits your change,
but the warning scrolls past in a long run. Committing first is what makes the
number mean what you think it means.

**Uncommitted work is unprotected against this workflow's own commands.** The
house standard for proving a guard is load-bearing is to break the
implementation, watch the test fail, and restore it — and the restore is
usually `git checkout -- <path>`, which discards every uncommitted edit in that
file, not just the deliberate break. That has bitten this repo: the
`lc.OnStarted(health.Up)` wiring was written, verified, and then destroyed by
the `git checkout --` that ended its own break-it experiment. Committed first,
the restore is exact and the experiment is free.

**It separates the change from what testing taught about the change.** "Add
`OnStarted`" and "kill the three survivors that exposed" are different claims,
and a reviewer can accept one while questioning the other. Squashed together
they read as a single confident step, which is the shape least likely to get
the survivor argument actually checked.
