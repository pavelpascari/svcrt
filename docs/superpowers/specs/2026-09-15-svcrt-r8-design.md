# svcrt R8 — `testkit`

**Status:** design for review
**Date:** 2026-09-15
**Scope:** milestone R8 of the svcgen/svcrt spec v0.2
**Predecessor:** [R7 design](2026-09-15-svcrt-r7-design.md) — circuit breaker
**Named as planned since:** R0, in `README.md`

---

## 1. Why, and what the evidence actually says

Eight milestones of test code wrote the same helpers repeatedly:

| helper | written in |
|---|---|
| capture log output, read it back | `config`, `logging`, `lifecycle`, `httpserver`, `kit`, `examples/orders` |
| stub upstream with canned responses | `httpclient`, `kit`, `examples/orders` (×4 files) |
| assert on a `contract.Coded` error | `contract`, `examples/orders` (×2 files) |

That is the case for `testkit`. It is also, read carefully, a weaker case than
it looks, and §2 says why.

## 2. `testkit` cannot serve svcrt's own core modules, and that is a policy choice

The obvious move — have `logging`'s tests use `testkit` — is **technically
possible and deliberately forbidden**.

Technically possible: Go permits the module cycle, because an external test
package breaks the cycle at package level. Verified with a two-module
experiment where `lib`'s `package lib_test` imports a `tk` that imports `lib`;
it builds and tests cleanly.

Deliberately forbidden: a test-only dependency still lands in `require`.
Measured, `go list -m all` then returns **2** lines, and
`scripts/ci.sh` fails any non-exempt module returning more than 1. Letting core
modules use `testkit` means exempting all seven, which does not bend the
zero-dependency invariant so much as delete it.

So the duplication table in §1 overstates the in-repo win. Of the six modules
duplicating log capture, only **`kit` and `examples/orders`** can adopt
`testkit` — the two that already import siblings. The rest keep their
hand-rolled helpers, and that is the cost of the promise that adopting a core
module costs you nothing.

**`testkit` is for consumers of svcrt.** The exemplar is the only in-repo proof
that it works, which makes §6's migration the acceptance test rather than a
tidy-up.

## 3. `testkit` defines its own `TB`, and never imports `testing`

```go
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Cleanup(func())
}
```

Not `testing.TB`, for a reason that decides the whole API: `testing.TB` carries
an unexported `private()` method (`testing.go:917`) precisely to prevent outside
implementations, and 23 exported methods besides. **`testkit`'s own tests must
be able to assert that its assertions fail when they should**, which requires a
fake — and a fake of `testing.TB` is impossible.

A four-method interface is satisfied by `*testing.T`, `*testing.B` and a
ten-line fake alike.

It also buys a property worth having: **`testkit` does not import `testing` at
all**, so a consumer who references it outside a `_test.go` file does not drag
in test-flag registration. `net/http/httptest` sets the same precedent.
Criterion 2 asserts this structurally rather than trusting it.

## 4. Surface

```go
// Log capture
func Logger(tb TB, ex ...logging.Extractor) (*slog.Logger, *Records)

type Record struct {
	Level   string
	Message string
	Attrs   map[string]any
}

type Records struct{ /* ... */ }
func (r *Records) All() []Record
func (r *Records) Last() (Record, bool)
func (r *Records) Find(key string, value any) []Record

// Stub upstream
func Upstream(tb TB, script ...Response) *Server

type Response struct {
	Status int
	Body   string
	Header http.Header
}
func Status(code int) Response
func JSON(code int, body string) Response

type Server struct{ /* ... */ }
func (s *Server) URL() string
func (s *Server) Requests() int

// contract assertions
func AssertCode(tb TB, err error, want string)
func AssertParams(tb TB, err error, want map[string]any)
func Code(err error) (string, bool)
```

### 4.1 `Logger` takes extractors, so telemetry stays out of `testkit`

`logging.New` is `New(w io.Writer, opts Options, ex ...Extractor)`, and
`Logger` forwards the variadic through. A consumer testing trace correlation
writes `testkit.Logger(t, telemetry.LogExtractor())` — and **`testkit` never
imports `telemetry`**, so it never pulls OTel.

That is worth the small awkwardness. A `testkit` that imported `telemetry` to
offer a trace-id assertion would make every consumer pull the OTel module graph
to assert on a log line.

`Records` is written from the logging goroutine and read from the test
goroutine, so it is mutex-guarded. Its tests run under `-race -count=10`;
`testkit` joins `COUNT_MODULES`.

### 4.2 `Upstream` repeats its last response

`Upstream(tb, Status(503), Status(503), JSON(200, "{}"))` answers 503, 503, then
200 **for every request thereafter**. Repeat-last rather than exhaust-and-fail
because both shapes a test needs fall out of it: "fails twice then recovers" is
the three-element script above, and "is simply down" is `Upstream(tb,
Status(500))`.

An empty script answers 200 with an empty body — the do-nothing upstream a test
uses when it only cares that a request arrived.

The server registers its own shutdown through `tb.Cleanup`, which is why
`Cleanup` is in the `TB` interface at all.

### 4.3 The `contract` assertions are where the wire rule gets enforced

`AssertCode` and `AssertParams` exist because the project's oldest cross-cutting
decision — the wire carries codes and language-neutral scalars, never
server-authored prose — is currently enforced by nothing but review. A helper
that makes "assert the code, assert the params" the path of least resistance is
how that rule survives contact with consumers who never read the spec.

`Code(err) (string, bool)` is the non-asserting accessor, for a test that wants
to branch rather than fail.

## 5. Module placement

`testkit` requires `contract` and `logging`, and is therefore the **second**
sibling-importing module after `kit`. R6's CI gate derives that bucket from disk
— a module requiring a sibling at `v0.0.0` runs in workspace mode rather than
`GOWORK=off` — so it should absorb `testkit` with no edit. **Verify that rather
than assume it**; R6 wrote the derivation precisely so a later module would not
be silently ungated, and this is the first test of that claim.

`conventions.md` §11 names the exceptions to the zero-dependency rule and gains
a third entry.

## 6. The exemplar is the acceptance test

`examples/orders` and `kit` migrate to `testkit`, and their suites must pass
with **assertions unchanged in meaning**. Their hand-rolled helpers are what
`testkit` is extracted from, so a test that needs rewriting is evidence the
extraction changed behaviour.

The migration is also the only in-repo evidence `testkit` is usable at all
(§2). A `testkit` nothing consumes is a guess about what consumers want.

## 7. Acceptance criteria

1. `testkit/go.mod` requires `contract` and `logging` and **nothing else**.
2. `testkit` does **not** import `testing`, `telemetry`, `httpserver`,
   `httpclient`, `resilience` or `kit` — asserted structurally, not by reading.
3. Every core module still has zero requires and imports no sibling. `testkit`
   must not become a reason for that to change.
4. `Logger` forwards its extractors: a record made inside a span carries the
   attributes the supplied extractor produces, proven with a stub extractor
   rather than with `telemetry`.
5. `Records` is safe under concurrent writes — asserted with concurrent logging
   under `-race`, and `testkit` is in `COUNT_MODULES`.
6. `Upstream` repeats its last response: a 3-element script answers the 4th and
   5th requests with the 3rd response. An empty script answers 200.
7. `Upstream` shuts down via `tb.Cleanup` without the test calling anything.
8. `AssertCode` **fails** on a mismatched code, a nil error, and a non-`Coded`
   error — proven with a fake `TB` that records the failure, not by inspection.
9. `AssertParams` fails on a missing key, a differing value, and an error that
   is `Coded` but not `Detailed`.
10. `examples/orders` and `kit` migrate, with assertions unchanged in meaning
    and every pre-existing test passing.
11. 100% statement coverage on `testkit`; mutation at or above 0.85, survivors
    killed or justified with **executed** evidence.
12. `conventions.md` §11 amended in place to name `testkit` as a third
    exception, distinguishing it from `kit` (both import siblings) and from
    `telemetry` (external dependency).
13. `./scripts/ci.sh` exits 0 across all 12 modules, with `testkit` in the
    workspace bucket **and visibly exercised**, not silently skipped.

## 8. Testing notes

**A test helper library has an unusual obligation: its failures must be
tested.** An assertion that never fails is worse than no assertion, and this
repo has now found five tests that could not fail — every one by running a
mutation rather than by reading. Criteria 8 and 9 exist because `AssertCode`
passing when it should pass proves almost nothing; the fake `TB` proving it
fails when it should is the real test.

**`testkit`'s own tests may not use `testkit`.** The fake `TB` is its own, and
the recursion stops there.

## 9. Out of scope

Span-context builders and trace-id assertions (§4.1 — they would pull OTel into
every consumer's test dependencies). A `RoundTripper` func adapter: written
three times in this repo and three lines each, which is below the threshold
where a dependency pays for itself. Golden-file helpers, HTTP request builders,
and anything for `lifecycle` ordering — no measured duplication, so no evidence
of need.
