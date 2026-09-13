# svcrt R4 — attribute ownership

**Status:** approved design, ready for implementation planning
**Date:** 2026-09-13
**Scope:** milestone R4 of the svcgen/svcrt spec v0.2
**Predecessor:** [R3 design](2026-09-13-svcrt-r3-design.md) — `resilience`
**Successor:** R5 — `telemetry`, which this milestone clears the way for

---

## 1. Why this exists

R4 ships no new module and no new behaviour. It moves four constants, one
middleware, and deletes two constants, so that **every module owns the
vocabulary for the attributes it emits**.

The principle surfaced while designing `telemetry`. `logging` declares
`KeyTraceID = "trace_id"` and `KeySpanID = "span_id"` — and never uses either.
Both have **zero** non-test producers anywhere in the repo. Meanwhile
`logging/handler.go` states the package's own design:

> It exists so correlation works without logging depending on a tracing
> library: svcrt/telemetry supplies an Extractor that reads span context, and
> **this package never learns what a span is.**

A package that never learns what a span is should not be naming one. The
constants are a promise `logging` cannot keep, held on behalf of a module that
does not exist yet.

Following that thread found three more misplacements, and one simplification
nobody was looking for (§5).

## 2. Where each attribute belongs

| key | who produces it | today | after |
|---|---|---|---|
| `trace_id` | a span | `logging` (0 producers) | **deleted**; `telemetry` declares it at R5 |
| `span_id` | a span | `logging` (0 producers) | **deleted**; `telemetry` declares it at R5 |
| `method` | the access-log middleware | `logging` | **`httpserver`** |
| `route` | the access-log middleware | `logging` | **`httpserver`** |
| `status` | the access-log middleware | `logging` | **`httpserver`** |
| `duration_ms` | the access-log middleware | `logging` | **`httpserver`** |
| `code` | a `contract.Coded` error | `logging` | **`contract`** |

### 2.1 `trace_id` and `span_id` are deleted, not moved

`telemetry` does not exist until R5, so there is nowhere to move them to.
Deleting is also the more honest operation: an unused exported constant is not
an asset being relocated, it is a name reserved for work not yet done.

`logging`'s tests reference `KeyTraceID` 28 times, all in `handler_test.go`,
and all as a **stand-in** for "some attribute an Extractor produced". They are
testing the seam, not the key. They become string literals, which is what they
always meant.

R5's `telemetry` declares its own `KeyTraceID`/`KeySpanID`. Neither module
imports the other, in either direction — the `Extractor` seam is
`func(context.Context) []slog.Attr`, entirely stdlib, and needs no shared
vocabulary to work.

### 2.2 `code` moves rather than being deleted

Unlike the trace keys, `code` **has** a producer: `examples/orders/server.go`
emits it on the error path. Its consumer is the application, and it names
`contract.Coded`'s concept. `contract` imports only `context`, so a string
constant costs it nothing.

`examples/orders/server_test.go` already carries a comment recording that
`logging.KeyCode` "had no producer anywhere in the repo" — an earlier milestone
noticed the same smell from the other end and made the exemplar its producer.
This milestone finishes that thought by putting the name where the type is.

## 3. `logging.Middleware` moves to `httpserver`

```go
// before
func logging.Middleware(l *slog.Logger) func(http.Handler) http.Handler

// after
func httpserver.AccessLog(l *slog.Logger) Middleware
```

It is an HTTP middleware that reads `r.Method`, `r.Pattern` and the response
status. Every one of those is `httpserver`'s domain, and **it needs nothing
from `logging`** except the four key constants moving with it: `*slog.Logger`
is stdlib and the returned func type is unnamed.

The move is close to free in both directions:

- `httpserver` gains one **stdlib** import, `log/slog`. No new dependency.
- `logging` loses `net/http` **entirely**. Its imports drop to `context`, `io`,
  `log/slog`, `slices`, `time` — a logging library with no HTTP in it, which is
  what it always claimed to be.

Renamed to `AccessLog` because `httpserver` already has a type named
`Middleware`, and a function and a type of the same name in one package invites
exactly the confusion §5 is about to remove.

`logging/middleware_test.go` moves with the code.

## 4. What `logging` keeps

`New`, `Options`, `Extractor`, `NewHandler`, and the `Extractor` contract —
including its unusually explicit no-panic clause. Nothing about HTTP, nothing
about tracing. The package becomes exactly what its own doc comment describes.

## 5. `conventions.md` §2 gets shorter, and that is the result rather than tidying

§2 currently reads **"`Middleware` means four things, on purpose — never a
fifth"** and enumerates:

1. `contract.Middleware` — a generic call-level type
2. `logging.Middleware` — *a constructor returning one of these*
3. `httpserver.Middleware` — wraps an `http.Handler`
4. `httpclient.Middleware` — wraps an `http.RoundTripper`

Entry 2 is the odd one: three types and a function. It existed only because a
constructor lived in a different package from the type it returned. Once
`AccessLog` sits beside `httpserver.Middleware`, the ambiguity it documented
does not exist, and §2 becomes **three things, all of them types**.

The section must be **amended in place**, not annotated — that file's preamble
requires it and prior reviews have specifically checked it. The rule R3 added
there (a module shipping middleware for someone else's named type returns the
bare unnamed func type) is unaffected and stays.

## 6. Why now

**No module is tagged.** `git tag` is empty across the repo, so removing an
exported name costs nothing today and becomes a breaking release the moment
anything ships at v1. The same work after tagging means a major version bump
on three modules.

Doing it before `telemetry` rather than during also keeps two stories in two
reviews: this milestone is a refactor with a green suite either side, and R5 is
new behaviour. Bundling them would mean reviewing a new module and a
three-module renovation in one diff.

## 7. Acceptance criteria

1. `logging` exports no `KeyTraceID` and no `KeySpanID`, and `grep -r` finds no
   reference to either anywhere in the repo.
2. `logging` does not import `net/http`. Verified by its import block, not by
   inspection of what uses it.
3. `httpserver` exports `AccessLog(l *slog.Logger) Middleware` and the four
   keys `KeyMethod`, `KeyRoute`, `KeyStatus`, `KeyDurMS`.
4. `contract` exports `KeyCode` and still imports **only** `context`.
5. `examples/orders` builds and passes against the moved names, with its
   assertions on `route`, `status` and `code` unchanged in meaning.
6. Every library module still has **zero `require` directives**; `GOWORK=off`
   builds each standalone.
7. `conventions.md` §2 is amended in place to three entries, with no surviving
   sentence claiming four.
8. Coverage and mutation floors hold for every touched module: `logging`,
   `httpserver`, `contract`, `examples/orders`.
9. `./scripts/ci.sh` exits 0 across all 9 modules before and after; `go vet`
   clean; `gofmt` silent.

## 8. Testing

This milestone adds no behaviour, so its correctness criterion is that
**nothing changes except where things live**. The access-log middleware's tests
move with it and must pass unmodified apart from the package clause and the
constructor name — a test that needed rewriting to accommodate the move would
be evidence the move changed behaviour.

`logging`'s `handler_test.go` is the exception: its 28 uses of `KeyTraceID`
become literals. That is a mechanical substitution and the assertions are
otherwise untouched.

**A risk worth naming.** A pure-move refactor is exactly where a silent
behaviour change hides, because the diff looks like relocation and reviewers
read it as such. The mutation gate is the check that matters here: `httpserver`
and `logging` both have floors, and a moved middleware that lost an assertion
shows up as a survivor even when the diff looks clean.

## 9. Out of scope

`request_id` — genuinely unsettled. If it arrives in a header and rides in
baggage it is `telemetry`'s; if `httpserver` mints one when absent it is
`httpserver`'s; most services want both. No constant for it exists today, so
this milestone does not invent one. R5 decides.

Service name and version — per-process constants set once with `slog.With` at
construction, not per-record derivations. In OTel terms they are Resource
attributes the SDK carries. No constant is needed and none is added.
