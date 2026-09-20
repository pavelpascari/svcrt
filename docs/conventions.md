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

## 2. `Middleware` means three things, on purpose — never a fourth

R0 declared two spellings of `Middleware` — `contract.Middleware[Req, Res]`
and a constructor, `logging.Middleware`, for the transport-level shape it was
still only describing in prose — and warned that giving that transport-level
shape its own named type, alongside `contract.Middleware[Req, Res]`, would be
one too many. R1 added that type anyway, deliberately, once `httpserver`
needed a name, `httpserver.Middleware`, for the shape `logging.Middleware`
had been building all along. R2 added `httpclient.Middleware`, the
client-side mirror of `httpserver.Middleware`, once `httpclient` needed the
same shape on `http.RoundTripper` instead of `http.Handler`. R3 needed that
exact shape again, for `resilience`'s retry and timeout middleware, and did
not declare a type of its own for it — see below for why declaring one would
have been a mistake, not just a style preference. R4 then removed one of
R0's original two: `logging.Middleware` had counted as its own entry only
because its constructor lived in a different package from the type,
`httpserver.Middleware`, it returned. Moving that constructor into
`httpserver` as `AccessLog` closed the gap — a same-package constructor
returning its own package's named type is just an instance of that type, not
a spelling of its own, so it needs no separate entry here. Three stand:

- `contract.Middleware[Req, Res]` is a **generic type**: the call-level seam,
  `func(Handler[Req, Res]) Handler[Req, Res]`. It exists so hand-written and
  generated code spell the same thing the same way.
- `httpserver.Middleware` is the plain **transport-level type**:
  `func(http.Handler) http.Handler`. It has no logic of its own; it names the
  shape `httpserver.AccessLog` (and every other transport-level constructor)
  builds, so `Chain` and its callers have something concrete to write down.
  `AccessLog(l *slog.Logger)` is the constructor that returns it, sitting in
  the same package as the type it returns — which is exactly why it needs no
  entry of its own here.
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
  the package has exactly one and lives apart from the `Middleware` type
  itself; name it for its job (`AccessLog`, `RecoverMiddleware`,
  `RequestIDMiddleware`) when it has several, or when — as with
  `httpserver.AccessLog` — the constructor shares a package with the type it
  returns, where the name `Middleware` is already taken by the type.
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
package is a different case, not a fourth spelling: it must return the bare,
unnamed func type instead of a same-shaped type of its own. `resilience` is
the worked example. `Retry`, `Timeout` and `Breaker` wrap
`http.RoundTripper` — exactly `httpclient.Middleware`'s shape — but
`resilience` depends on nothing outside the standard library, so it cannot
import `httpclient` to name its return type `httpclient.Middleware`. Declaring
`type Middleware func(http.RoundTripper) http.RoundTripper` in `resilience`
instead, for tidiness, would look harmless and would not compile through:
`httpclient.Chain(resilience.Retry(p))` requires the argument to be
assignable to `httpclient.Middleware`, and Go does not assign one named type
to another merely because their underlying types match — two named types
with identical underlying types are still different types, full stop. An
*unnamed* func type carries no such restriction: it is assignable to any
named type sharing its underlying type, which is exactly the property
`resilience` needs and a same-shaped named type would throw away. So `Retry`,
`Timeout` and `Breaker` return the bare
`func(http.RoundTripper) http.RoundTripper`, never a named type of their own
— and that is the general rule for this case: a module shipping middleware
for a type it does not own returns the bare func type, precisely so neither
module has to import the other.
`httpserver.AccessLog` is the contrasting case, not an exception to it:
it returns `httpserver.Middleware`, a *named* type, and that is fine
precisely because `AccessLog` lives in `httpserver` itself — the same-package
case this rule exists to distinguish from `resilience`'s cross-package one.

R5's `telemetry` is the **second** module to need that rule, and it needs it
three times over in one package: `Server` returns the bare
`func(http.Handler) http.Handler`, `Client` the bare
`func(http.RoundTripper) http.RoundTripper`, and `LogExtractor` the bare
`func(context.Context) []slog.Attr`. They are assignable to
`httpserver.Middleware`, `httpclient.Middleware` and `logging.Extractor`
respectively while `telemetry` imports none of those three modules — which it
must not, because it is the one module carrying dependencies (§11) and a
service adopting `logging` alone must not inherit OpenTelemetry through it.
`LogExtractor` extends the rule past middleware: the shape being targeted is a
plain extractor function, but the Go fact underneath is identical, so the same
answer applies to any exported constructor returning a func type owned by a
package it does not import. `examples/orders/telemetry_test.go` asserts all
three at compile time, which is the only place the assignability can be
checked — inside `telemetry` there is nothing to assign to.

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
`httpserver`, `resilience`, `telemetry`, `kit` and `testkit` run this way; the
other library modules stay at `-count=1`.

Two of those six are there by judgement rather than by the heuristic below,
and both would have been silently absent if the heuristic were the whole gate.
`telemetry` (R5) matches none of the grepped constructs, yet its middleware
closures capture one histogram and one tracer shared by every concurrent
request. `kit` (R6) is three constructors and a slice literal and holds no
shared mutable state of its own, but what it produces is a single
`*http.Client` shared across every goroutine a service has, and it is the only
place the composed chain runs as a whole — `resilience` and `telemetry` each
get their ten runs unwired, so a race that exists only between them is visible
nowhere else. The composition is the artifact, so the composition gets the
gate.

`testkit` (R8) is the first entry the inverse heuristic below would have
caught unaided: `Records` and `Server` are both mutex-guarded, so `sync.`
matches its non-test source and CI demands either membership or a
`COUNT_EXEMPT` entry. It belongs on the merits regardless — `Records` is
written by whichever goroutine logged and read by the test goroutine, and
`Server`'s request counter is written by handler goroutines and read by the
test. A test helper that races is worse than a racing library, because the
flake it produces is attributed to the code under test rather than to the
helper.

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

## 11. Core modules have zero dependencies; there are three exceptions, and they are not the same kind

`contract`, `config`, `logging`, `lifecycle`, `httpserver`, `httpclient` and
`resilience` have **zero `require` directives**. A service can adopt any one of
them without inheriting anything. This had never been written down — five
milestones enforced it by habit, and `grep` finds no section stating it —
which was survivable only while no module ever wanted an exception.

There are now three exceptions, and reading them as a set of equivalents would
lose what matters about the second and the third:

- **`telemetry` takes an external dependency** — the OpenTelemetry API. It
  pulls something into a service from outside the repo.
- **`kit` takes sibling dependencies**, and is the first module in svcrt ever
  to do so. It pulls nothing in from outside that its siblings do not already
  carry; what it introduces is an edge *between* svcrt modules, where there
  had been none.
- **`testkit` takes sibling dependencies too, but for testing** — `contract`
  and `logging` — and carries a limitation neither of the others has: **no
  core module may use it.** That is not an oversight, and it is the first
  question a reader has, so it is answered in full below.

The second is a bigger change than the first, because the property at risk is
different. An external dependency is a cost you can read off a `go.mod`. A
sibling edge is a direction: once `A` imports `B`, everything `B` ever requires
is something `A`'s consumers get too, whether or not they wanted it — which is
also why §2's unnamed-func-type rule exists.

`telemetry` requires the OpenTelemetry API — `otel`, `otel/trace`,
`otel/metric` — and is the only module that does. (`propagation`,
`attribute`, `codes`, `semconv` and `baggage` are packages inside `otel`;
`metric/noop` is inside `otel/metric`. Three requires, not eight.)

The rule was never "no dependencies ever". It was "adopting a core module costs
you nothing", which holds exactly as well when the module you inherit OTel from
is the one whose entire job is speaking OTel. A tracing library that refused to
depend on a tracing API would reimplement W3C tracecontext, which is not
independence but a second, worse implementation of a standard.

The exemption is for the **API**, never the SDK. Choosing an exporter, sampler
and resource is an application decision with an application's lifetime, and a
library that makes it takes the choice away from every consumer. No test in
`telemetry` imports `otel/sdk/*`; tests inject stub providers instead.

The exception is also why §2's unnamed-func-type rule is load-bearing rather
than stylistic here. If `telemetry` could import `logging`, `httpserver` and
`httpclient` to name its return types, then adopting `logging` would drag
OpenTelemetry in behind it — not because `logging` wanted it, but because of
which direction the import happened to point.

### `kit`, and the sibling rule

**No core module imports a sibling.** `kit` is the only module permitted to,
and it imports four: `httpclient`, `resilience`, `telemetry`, `logging`.

The argument has the same shape as `telemetry`'s. A module whose entire job
requires the dependency takes the cost, and it is opt-in so nobody else pays.
`kit` exists to encode the three middleware orderings R5 proved fail silently
when inverted, and a composition module cannot compose without importing the
things it composes. Nothing in svcrt imports `kit`; a service that ignores it
inherits exactly what it did before, and one that adopts it deletes one import
to leave.

What that costs, stated plainly: a service on `kit` cannot upgrade
`resilience` independently of `kit`. It upgrades `kit`, which pins the set.
That is the actual trade, and it is why `kit` must stay thin enough to leave.

`kit` deliberately does **not** import `httpserver`. A service builds one
handler chain in one visible place; it builds a client per upstream, and it is
the repeated case that earns a constructor. `ci.sh` asserts that omission
structurally, because a deliberate omission nothing enforces is a comment.

The sibling rule is newly *enforced*, not newly true. No core module has ever
imported another — five milestones of habit, with nothing checking it, exactly
as the zero-requires rule was before R5. R6 introduced the first deliberate
violation, which is the moment to gate it rather than the moment to stop
caring: a core module that starts requiring a sibling now fails CI by name.

### `testkit`, and why svcrt's own core modules may not use it

`testkit` is the third exception and the second sibling importer. It requires
`contract` and `logging`, and nothing else. A helper that asserts on a
`contract.Coded` error cannot do it without `contract`; a helper that captures
log records cannot do it without `logging`. The argument has the same shape as
`kit`'s — a module whose entire job requires the dependency takes the cost,
opt-in, so nobody else pays.

**The limitation, stated plainly: a core module may not adopt `testkit`, not
even in a `_test.go` file.** Go permits it — an external test package
(`package logging_test`) breaks the import cycle, and a two-module experiment
confirms it builds and tests cleanly. The bar it fails is not the compiler's,
it is this section's: **a test-only dependency is still a `require`.**
`go list -m all` then returns 2 lines for that module, and the zero-requires
gate in `ci.sh` fails it by name. Letting the core modules use `testkit` means
exempting all seven, which does not bend the zero-dependency invariant so much
as delete it.

The cost of that decision is visible and was measured before it was taken.
Six modules in this repo hand-roll log capture; only `kit` and
`examples/orders` — the two that already import siblings — can adopt
`testkit`, so five keep their hand-rolled helpers forever. That duplication is
the price of "adopting a core module costs you nothing", and it is the right
way round: the duplication is paid once, by this repo, by people who can see
it; the alternative is paid by every service that ever imports `logging`.

**`testkit` is for consumers of svcrt.** `kit` and `examples/orders` are the
only in-repo evidence it works at all, which is why their migration was the
acceptance test for R8 rather than a tidy-up — a `testkit` nothing consumes is
a guess about what consumers want.

Two consequences of that audience shape the package:

- **`testkit` never imports `testing`.** Its `TB` is a four-method interface
  of its own, satisfied by `*testing.T` and `*testing.B`. Importing `testing`
  from a non-test package registers test flags in any consumer that references
  it, and `net/http/httptest` sets the precedent. It is also what makes the
  package testable: `testing.TB` carries an unexported `private()` method
  specifically to prevent outside implementations, so a fake of it cannot be
  written — and an assertion that cannot be shown to fail is worse than no
  assertion.
- **`testkit` never imports `telemetry`.** `Logger` forwards
  `...logging.Extractor` so a consumer supplies `telemetry.LogExtractor()`
  themselves. A `testkit` that offered a trace-id assertion directly would make
  every consumer pull the OTel module graph to assert on a log line — §2's
  unnamed-func-type rule, applied one module further out.

### How it is enforced

Not by this section. `scripts/ci.sh` asserts it per module, and it asserts
**both directions**:

- A module **not** in `DEP_EXEMPT` must produce exactly one line from
  `GOWORK=off go list -m all` — itself, and nothing else.
- A module **in** `DEP_EXEMPT` must produce **more than one**. An exemption
  for a module that has quietly gone dependency-free is a stale exemption, and
  a stale exemption is a lie in the gate: it reads as a considered decision
  while permitting anything.

`DEP_EXEMPT` lives in `scripts/lib.sh` and is shaped `<module>=<why>`, a
sentence and not a bare name, for the same reason `COUNT_EXEMPT` is (§5): once
a list is just names, "someone forgot" and "someone decided" become
indistinguishable. `lib.sh` additionally asserts, forward, that every entry
names a module that exists on disk and carries a reason; the inverse half
needs `go list -m all` and so lives in the loop that already pays for that
call, reached through `dep_exempt_reason`.

The sibling rule is asserted in `scripts/lib.sh`, both directions, against the
`go.mod` files on disk:

- A module whose `go.mod` requires **any** `github.com/pavelpascari/svcrt/*`
  must be named in `SIBLING_ALLOWED` — shaped `<module>=<why>`, same as the
  lists above and for the same reason. Today that is `kit` and `testkit`, and
  nothing else.
- An entry in `SIBLING_ALLOWED` must name a module on disk, carry a reason,
  and name a module that actually requires a sibling. A permission for a
  module that requires none is the same lie a stale `DEP_EXEMPT` entry tells.

One consequence is worth knowing, because it changes how CI runs. `kit` and
`testkit` require their siblings at `v0.0.0`, a version that resolves to
nothing while this repo is untagged, so they **cannot be built under
`GOWORK=off`** — and
`GOWORK=off` is how `ci.sh` proves every other module stands alone. That gate
encodes an invariant `kit` exists to violate: `kit` is not useful without other
svcrt modules present. So `ci.sh` derives the bucket from disk — a module
requiring a sibling at `v0.0.0` runs **with** the workspace, exactly as the
exemplars do and for the same reason — and it is self-healing: once the
siblings are tagged and those requires name a real version, the module resolves
standalone and drops back into the `GOWORK=off` loop with no exemption for
anyone to remember to remove. The two buckets are asserted to partition
`MODULES`, so a module cannot fall out of both and be silently untested.

A workspace-bucket module skips the zero-requires assertion, because it has
requires by construction. What replaces it: every non-sibling require must
already be required by one of the siblings it composes. A composition module
must not invent dependencies of its own, or adopting it would cost more than
adopting the pieces. That check matches on module path and not version, so it
does not see the version skew `GOWORK=off` exists to expose — that protection
comes back on its own when the siblings are tagged.

`scripts/release.sh` refuses to tag a module in the workspace bucket, and says
why: a tag would publish a `go.mod` no consumer outside this workspace can
resolve. Tag the siblings first.

`scripts/release.sh` applies the same two-way check through the same
`dep_exempt_reason`, for the reason §5 gives about `-count`: tagging is the
point after which a version is permanent, so it is the last place the policy
can be enforced and must not be weaker there than in CI. It was weaker until
R5 — it carried the original unguarded `[ "$n" -eq 1 ]` and would have refused
to tag `telemetry` forever. Nothing caught that, because no module has been
tagged yet, so the bug had no way to surface until the first person tried to
cut a `telemetry` release. Both scripts now read one list.

### Adding a fourth exception

A design change, not a judgement call. It needs the same argument the three
above got, written down here, plus its `DEP_EXEMPT` or `SIBLING_ALLOWED`
entry. "It only pulls in one small library" is not that argument: the cost is
paid by every service that adopts the module, and they are not in the room.

For a sibling edge specifically, the bar is higher, because the answer is
almost always §2's: return the bare `func` type and let the caller assign it,
so neither module imports the other. `telemetry.LogExtractor` and
`resilience.Retry` both cross module boundaries that way with zero requires
between them. A new sibling import needs to say why that is not available to
it — `kit`'s answer is that composition, not assignability, is the thing it
sells; `testkit`'s is that an assertion about a `contract` error has to name
`contract` to make it.

"It is only a test dependency" is likewise not an argument, and `testkit` is
the reason the sentence is here: a `require` added by a `_test.go` file is a
`require` like any other, counted by `go list -m all` and by the gate.

## 12. What CI runs automatically, and what it deliberately does not

Until R9 there was no `.github` directory. `scripts/ci.sh` had existed since
R0 and had only ever run in a developer's shell, so "ci.sh exit 0" in nine
milestone reports meant *exit 0 by hand, on a branch* — every merged PR
carried zero automated verification. That is the gap this section records
being closed, and it is worth recording because a gate nothing runs is
indistinguishable, in a report, from a gate that passed.

### Runs on every push to `main` and every pull request

`.github/workflows/ci.yml` runs two things, in order:

- **`./scripts/ci.sh`** — unchanged. It already did `gofmt -l` (tested for
  empty output, not exit status), `go vet`, `go test -race` at the `-count`
  this file's §5 assigns, the zero-dependency assertions in both directions
  (§11), the sibling-import gate in both directions, `contract`'s
  import-and-`go`-directive assertions, and the exemplar suites. It needed no
  changes; it needed to be *run*.
- **`./scripts/coverage.sh`** — the ratchet, below.

The Go version comes from `go-version-file: kit/go.mod` rather than a literal
in the workflow, so the toolchain is stated in one place and cannot drift out
of step with the modules.

`.github/workflows/compat.yml` builds and tests `contract` under a real Go
1.22 toolchain with `GOWORK=off`. `ci.sh` enforces that commitment by grepping
`contract/go.mod` for `go 1.22`, which verifies that we still *claim* it. The
`go` directive gates syntax, not stdlib APIs: a module declaring `go 1.22` can
call a Go 1.23 stdlib function and compile clean under a 1.25 toolchain. Only
a real 1.22 toolchain catches a post-1.22 stdlib call added to `contract`,
which is precisely the regression that commitment exists to prevent. Only
`contract` gets this job, because it is the only module making the promise.

### The coverage ratchet is a minimum, and its drift is printed, not failed

`coverage-floors.txt` records one floor per module; `scripts/coverage.sh`
derives the module list from `scripts/lib.sh` — `MODULES` and `EXEMPLARS`, the
same lists `ci.sh` and `release.sh` read — rather than keeping a second copy.
Four behaviours, and all four were demonstrated by breaking them:

- **Below floor → FAIL**, naming the module. The regression case, which is the
  whole point.
- **Above floor → PASS**, and the gap is printed. Coverage improving must
  never break a build.
- **A module on disk with no entry → FAIL.** A new module silently ungated is
  the failure §5's inverse assertion exists for, repeated.
- **An entry naming no module on disk → FAIL.** A rename otherwise leaves the
  gate matching nothing while CI still prints OK — the same lie a stale
  `DEP_EXEMPT` entry tells (§11).

The honest weakness, recorded rather than papered over: a floor left far below
actual stops catching regressions above it. A module that slips from 100% to
92% against a floor of 90% passes. Printing the gap on every run is the whole
mitigation, and it relies on somebody reading it and raising the floor — which
is a one-line edit to `coverage-floors.txt`. The alternative, failing a build
because coverage improved, was considered and rejected.

The floors are measured, never chosen. The ten library modules sit at 100%
because §4 and the spec require it; the exemplars are services rather than
libraries and carry the numbers they actually report.

### Mutation testing stays out of CI, on purpose

§9 and §10 describe mutation as a deliberate local act, and that does not
change. A full no-arg `scripts/mutation.sh` run has never completed in one
invocation, and per-module runs took minutes each during R7 and R8. A PR gate
that times out gets disabled rather than fixed, and a disabled gate is worse
than an absent one because it still appears in the list.

The cost is real and is stated rather than hidden: **nothing prevents a
mutation-score regression from merging.** The scores in
`docs/mutation-survivors.md` are claims, true as of the run that produced
them, re-checked only when somebody runs the script. The eight vacuous
assertions this repo has found were all found that way — by running a
mutation, never by reading — so the practice is load-bearing even though the
gate is not automated.
