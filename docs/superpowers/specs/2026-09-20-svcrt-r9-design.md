# svcrt R9 — polish: recovery, docs, and a CI that actually runs

**Status:** design for review
**Date:** 2026-09-20
**Scope:** milestone R9 of the svcgen/svcrt spec v0.2
**Predecessor:** [R8 design](2026-09-15-svcrt-r8-design.md) — `testkit`

---

## 1. Why these three, together

Three pieces of polish on ten finished modules. They are one milestone because
each is small and none is a new subsystem, and CI comes first because it is the
gate the other two land behind.

### 1.1 The finding that reorders the list

**There is no `.github` directory.** `scripts/ci.sh` has never run in CI. Nine
milestones have reported "ci.sh exit 0", and every one of those means *exit 0 in
a developer's shell, on a branch, by hand*. Every merged PR so far carried zero
automated verification.

That is the most expensive gap in the repo and it is not a code gap. Everything
this project has built to keep gates honest — two-way `DEP_EXEMPT`,
`SIBLING_ALLOWED`, `COUNT_MODULES` with an inverse assertion, six vacuous tests
found by mutation — sits behind a script nothing runs automatically.

### 1.2 A second gate that looks present and does not apply

`contract/go.mod` declares `go 1.22` as a deliberate compatibility commitment:
it is the module intended to freeze at v1 and be imported by every generated
service, so it must keep building on the oldest toolchain those services run.

`ci.sh:128` enforces that by **grepping for the string `go 1.22`**. Nothing has
ever built `contract` on Go 1.22. The commitment is verified by confirming we
still claim it, which is the exact shape of defect this repo has spent nine
milestones learning to distrust.

## 2. `httpserver.Recover`

```go
func Recover(l *slog.Logger) Middleware

const (
	KeyPanic = "panic"
	KeyStack = "stack"
)
```

Today nothing in svcrt recovers a panic — not a library, not the exemplar. Both
`httpserver.AccessLog` and `telemetry.Server` say so explicitly and deliberately:
"whether a panic becomes a 500 is the service author's decision, made in their
own middleware." That stays true. `Recover` is the middleware they were
referring to, shipped rather than left to every author to rewrite, and it is
opt-in: nothing composes it for you.

### 2.1 It goes INNERMOST, which is the opposite of the usual instinct

```
telemetry.Server -> AccessLog -> Recover -> your handler
```

Most frameworks put recovery outermost so it catches everything. Here that is
wrong, and measurably so.

A panic unwinds through deferred calls in inner frames first. With `Recover`
outermost, `AccessLog`'s deferred line and `telemetry.Server`'s deferred span
attributes both run **before** `Recover` writes the 500 — so the access line
records no status (R4 established it omits rather than invents one) and the span
records none either. The client gets a 500 that your own observability never saw.

Innermost, the 500 is written first and every middleware outside observes it: the
access line says 500, the span says 500 and is marked an error.

The cost is that a panic in `AccessLog` or `telemetry.Server` themselves is not
caught. That is the right trade — those two are svcrt's code and are tested; the
handler is not.

Like every other ordering in this repo, this gets a test that fails when it is
inverted (§5, criterion 4).

### 2.2 `http.ErrAbortHandler` must be re-panicked

`net/http` documents `panic(http.ErrAbortHandler)` as the way a handler aborts a
response without logging a stack — streaming code and proxies use it. A recovery
middleware that swallows it converts a deliberate, silent abort into a spurious
500 and a spurious error log.

`Recover` re-panics it unchanged, letting `net/http`'s own handling take over.
This is the single most commonly missed detail in hand-rolled recovery
middleware, which is a reason to ship one.

### 2.3 The panic value never reaches the client

The response is a bare 500 with no body. The panic value and stack go to the log
at `slog.LevelError` under `KeyPanic` and `KeyStack`.

This mirrors what the exemplar already does for uncoded errors — "the cause is
logged and never serialized" — and for the same reason: a panic message is
internal state, routinely carrying paths, query fragments and occasionally
credentials.

If the handler already wrote a response before panicking, `Recover` writes
nothing further. `httpserver` already has the unexported `statusWriter` that
tracks this, introduced for `AccessLog`; `Recover` reuses it rather than
declaring a second one.

## 3. Documentation

Package documentation is already substantial in all ten modules. Two things are
missing.

### 3.1 Runnable examples — zero today

`Example` functions are the only documentation Go compiles. An example with an
`// Output:` comment is *executed* by `go test` and its output compared, so it
cannot silently rot the way a code block in a README does.

Each module gets at least one, covering its primary entry point. Where output is
non-deterministic — `logging` stamps a timestamp — the example uses
`Options.ReplaceAttr` to drop the varying field, which is itself the thing a
reader wants to know.

They are not decoration: criterion 8 requires `go test ./...` to run them, which
means a signature change that breaks an example breaks the build.

### 3.2 Offline docs

`go doc` already works offline with no setup and is the answer for a quick
lookup. For browsing, `scripts/docs.sh` runs `pkgsite` against the workspace.

Verified before specifying: `pkgsite` installed from
`golang.org/x/pkgsite/cmd/pkgsite`, pointed at this repo, serves every module —
`contract`, `resilience` and `kit` all returned HTTP 200 with rendered docs, and
it discovered `lifecycle` and `examples/orders` through the workspace without
being told about them.

The script must degrade honestly: if `pkgsite` is not installed it prints the
one-line `go install` command and the `go doc` alternative, rather than failing
with a bare "command not found".

## 4. CI

### 4.1 `ci.yml` — the gate that has never run

Runs on push and pull request: `./scripts/ci.sh`, then the coverage check.

`ci.sh` already does `gofmt`, `go vet`, `go test -race` with `-count=10` on the
four concurrent modules, the zero-dependency assertions, the sibling-import
gate, and the exemplar suites. It needs no changes — it needs to be *run*.

It takes over two minutes locally. The job carries an explicit timeout generous
enough not to flake and tight enough to notice a hang.

### 4.2 The coverage ratchet: a minimum, and drift is printed not failed

`coverage-floors.txt` records a floor per module. `scripts/coverage.sh`:

- **below floor → FAIL.** The regression case, which is the whole point.
- **above floor → PASS, and print the gap.** Coverage improving must not break
  a build. The gap is printed on every run so drift stays visible without
  costing churn.
- **module on disk but absent from the file → FAIL.** A new module silently
  ungated is the R3 failure repeated; this direction is not about strictness,
  it is about a gate applying at all.
- **name in the file matching no module → FAIL.** The inverse, for the same
  reason `COUNT_MODULES` asserts both directions.

The honest limitation, recorded because the alternative was considered and
rejected: a floor left far below actual stops catching regressions above it.
Printing the gap is the mitigation, and it relies on somebody reading it.

### 4.3 `compat.yml` — build `contract` on Go 1.22

A second job that actually builds and tests `contract` with Go 1.22, turning
§1.2's grep into a fact. It runs only on `contract`, because it is the only
module making the promise.

### 4.4 Mutation stays out of CI

A deliberate choice. A full no-arg run has never completed in one invocation,
and per-module runs took minutes each during R7 and R8. A PR gate that times out
gets disabled rather than fixed.

The cost is real and stated: nothing prevents a mutation-score regression from
merging, and the scores in `docs/mutation-survivors.md` are claims re-checked
only when someone runs the script. `docs/conventions.md` §9 already describes
mutation as a deliberate local act; that stays true.

## 5. Acceptance criteria

1. `httpserver.Recover` recovers a panicking handler, responds 500, and logs the
   panic value and stack at error level.
2. The response body contains **neither** the panic value nor any stack frame —
   asserted on the body, not by inspection.
3. `panic(http.ErrAbortHandler)` is **re-panicked**, not converted to a 500 and
   not logged as an error.
4. `Recover` composed innermost lets `AccessLog` observe status 500; a test
   fails if it is composed outermost.
5. A handler that already wrote a response before panicking does not get a
   second `WriteHeader` — no "superfluous WriteHeader" and the original status
   is preserved.
6. `Recover` returns the named `httpserver.Middleware` (it is declared in the
   package that owns the type, so unlike cross-module middleware there is no
   assignability constraint).
7. Every module has at least one `Example` function, and those with
   deterministic output assert it with `// Output:`.
8. `go test ./...` compiles and runs every example in every module.
9. `scripts/docs.sh` serves the workspace offline, and prints actionable
   instructions rather than failing when `pkgsite` is absent.
10. `.github/workflows/ci.yml` runs `scripts/ci.sh` and the coverage check on
    push and pull request.
11. `scripts/coverage.sh` fails below floor, passes and prints the gap above it,
    and fails in **both** directions on a module/floor mismatch. Each of those
    four behaviours is demonstrated by breaking it, not argued.
12. `.github/workflows/compat.yml` builds and tests `contract` on Go 1.22.
13. 100% statement coverage holds on every library module, `httpserver`
    included after `Recover` lands.
14. `./scripts/ci.sh` exits 0 across all 12 modules.

## 6. Testing notes

**`Recover`'s tests must assert on the response body**, not just the status.
Criterion 2 is a leak check, and "it returned 500" is true of both the correct
implementation and one that writes the panic message into the body.

**The `ErrAbortHandler` test needs care**: the assertion is that the panic
escapes, so the test recovers it itself and checks the value, and additionally
asserts nothing was logged. A test that only checks "no 500" would pass against
a middleware that swallowed it and wrote nothing.

**The coverage script's own failure modes are the thing to test** (criterion
11). A ratchet that cannot fail is the same defect as an assertion that cannot
fail, and this repo has found six of those — every one by running a mutation
rather than by reading. Break each of the four branches and observe it.

## 7. Out of scope

Inbound concurrency limiting, request body caps and rate limiting — the rest of
the inbound-protection gap. `Recover` is included here because a panicking
handler producing no response is a correctness bug rather than a hardening
feature; the others are hardening and deserve their own milestone.

Tagging and publishing. Mutation in CI (§4.4). Any change to `ci.sh`'s existing
assertions — this milestone runs that script, it does not rewrite it.
