# svcrt R3 — `resilience`

**Status:** approved design, ready for implementation planning
**Date:** 2026-09-13
**Scope:** milestone R3 of the svcgen/svcrt spec v0.2
**Parent spec:** svcgen/svcrt v0.2 (§9, §12)
**Predecessor:** [R2 design](2026-09-13-svcrt-r2-design.md) — `httpclient`

---

## 1. Scope and rationale

R3 delivers **one module**: `resilience`. It ships `Retry`, the `Backoff`
policies retry needs, and a per-attempt `Timeout`, all as middleware over
`http.RoundTripper`.

R2 built a client that calls a dependency correctly. R3 is what happens when
that dependency is briefly not there.

### 1.1 No circuit breaker

R2's §1.1 deferred the breaker and the reasoning still holds: it is a concurrent
state machine — closed/open/half-open, a failure window, a cooldown clock, and
controlled probing while open — and it is shared mutable state on the request
path, which is the profile that needed the `-race -count=10` gate to catch R1's
dropped-fatal race behind 100% coverage and a 0.97 mutation score.

It is also, importantly, **unrelated to this milestone's hard problem**. Retry's
difficulty is not backoff curves; it is knowing when retrying is *wrong* (§3).
Bundling the two would mean designing both under the attention budget of one.

### 1.2 Out of scope

Circuit breaking, bulkheads, hedged requests, rate limiting, deduplication keys,
transport-agnostic combinators (§9), and every part of the generator.

## 2. The signature is fixed by Go's assignability rules, not by taste

`resilience` has zero `require` directives, so it cannot import `httpclient`
to name its `Middleware` type — the same constraint R2 worked under, restated in
[R2 §2](2026-09-13-svcrt-r2-design.md).

R2 §4.2 asserted that a future `resilience` would "ship functions of this same
shape" and compose without a dependency arrow. That is true, but only under a
condition R2 did not state and this spec must:

**Every exported constructor returns the bare, unnamed type
`func(http.RoundTripper) http.RoundTripper`. `resilience` must NOT declare a
named `Middleware` type for these returns.**

Go assignability permits a value to be assigned to a named type with the same
underlying type only when at least one side is unnamed. Two *different* named
types with identical underlying types are not assignable. Verified:

```
resilience returns the unnamed func type  -> httpclient.Chain(resilience.Retry(p))  compiles
resilience returns its own named type     -> cannot use RetryNamed() (value of func type
                                             resilienceMiddleware) as clientMiddleware
                                             value in argument to Chain
```

So the zero-import composition R2 designed for works — and would have stopped
working the moment `resilience` declared `type Middleware func(...)` for
tidiness. This is recorded because it looks like a style choice and is not.

`Backoff` *is* a named type (§4). Nothing outside this module needs to assign to
it, so the constraint does not apply there.

## 3. Retry is mostly about refusing to retry

Three gates, each independently sufficient to make a retry wrong. All three are
checked before any backoff is computed.

### 3.1 Replayability — the silent one

`http.Request.Body` is consumed by the first attempt. Replaying requires
`GetBody`, and **`http.NewRequest` only populates it for body types it
recognises**. Verified at go1.26:

```
nil body           GetBody==nil? true    ContentLength=0
strings.Reader     GetBody==nil? false   ContentLength=5
bytes.Reader       GetBody==nil? false   ContentLength=5
bytes.Buffer       GetBody==nil? false   ContentLength=5
opaque io.Reader   GetBody==nil? true    ContentLength=0

naive replay of an opaque body: first="hello" second=""
```

A retry that simply re-sends the request therefore transmits an **empty body**
on every attempt after the first — no error, no log line, a well-formed POST
carrying nothing. That is the same failure shape as R2's `ForceAttemptHTTP2` and
`Proxy` losses: invisible at the call site, and worse than failing outright,
because the server accepts it.

**Rule:** if `req.Body != nil && req.GetBody == nil`, do not retry. Return the
first result unchanged. Each attempt that *does* proceed calls `GetBody()` for a
fresh reader rather than reusing `req.Body`.

### 3.2 Idempotency — the semantic one

A lost response does not mean the request did not happen. Retrying a POST whose
response was dropped in transit duplicates its effect.

**Rule:** `GET`, `HEAD`, `PUT`, `DELETE`, `OPTIONS` and `TRACE` are retried by
default. `POST` and `PATCH` are not. `Policy.RetryMethods` overrides the set
explicitly, for the endpoint that is idempotent by key.

This gate is semantic and unverifiable by inspection — which is exactly why the
default is conservative and opting in is deliberate.

### 3.3 Draining — the expensive one

A retry discards the response of every failed attempt. Closing a response body
without reading it to EOF does not return the connection to the pool. Measured
against an `httptest.Server` counting `StateNew`:

```
close only (no drain):   5 TCP connections for 5 attempts
drain then close:        1 connection
```

So a retry loop that does not drain opens a fresh TCP connection per attempt,
paying a handshake each time — precisely the churn R2 raised
`MaxIdleConnsPerHost` to 100 to prevent. **R3's headline feature would silently
defeat R2's headline default.**

**Rule:** every discarded response is drained to EOF and closed before the next
attempt. The drain is bounded (§9 open question 1).

## 4. Surface

```go
func Retry(p Policy) func(http.RoundTripper) http.RoundTripper
func Timeout(d time.Duration) func(http.RoundTripper) http.RoundTripper

type Policy struct {
	MaxAttempts  int      // 0 -> 3 (i.e. one initial try plus two retries)
	Backoff      Backoff  // nil -> Jitter(Exponential(100*time.Millisecond, 2*time.Second))
	RetryMethods []string // nil -> GET, HEAD, PUT, DELETE, OPTIONS, TRACE
	RetryIf      func(*http.Response, error) bool // nil -> §4.1
}

type Backoff func(attempt int) time.Duration

func Constant(d time.Duration) Backoff
func Exponential(base, max time.Duration) Backoff
func Jitter(b Backoff) Backoff
```

`Timeout` is a separate middleware rather than a `Policy` field because it
applies **per attempt** and composes independently: `Chain(Retry(p), Timeout(d))`
gives each attempt its own deadline, while `Chain(Timeout(d), Retry(p))` bounds
the whole retry sequence. Both are legitimate and the order expresses which one
you meant. A `Policy.Timeout` field could express only one.

### 4.1 What is retried by default

Any transport error (the `RoundTrip` call returned non-nil), plus HTTP **429,
502, 503 and 504**.

**Not 500.** A 500 is usually a genuine server-side bug, which repeats; retrying
it converts one failure into three and amplifies load on an already-broken
dependency. A caller who knows their 500s are transient sets `RetryIf`.

**Not 4xx** other than 429 — the request is wrong and will stay wrong.

## 5. Two things the clock forces

### 5.1 `Retry-After` outranks the backoff policy

On 429 and 503, a `Retry-After` header (delta-seconds or HTTP-date) is honoured
in place of the computed backoff. A server saying when to come back is better
information than any local curve, and ignoring it is how a thundering herd
forms. A malformed value falls back to the policy rather than erroring.

### 5.2 Backoff never sleeps past the caller's deadline

If the request context has less time remaining than the computed delay, the
retry loop returns the last result immediately rather than sleeping into an
attempt guaranteed to be cancelled. Sleeping first and failing second wastes the
caller's remaining budget and reports a less useful error.

The sleep itself is cancellable: it selects on the context, so a cancelled
request aborts mid-backoff rather than after it.

## 6. The exemplar

`examples/orders` already calls a pricing upstream through `httpclient` (R2 §5).
R3 adds retry to that call and proves it works end to end:

```go
pricing := httpclient.New(httpclient.Options{
	Middleware: httpclient.Chain(
		resilience.Retry(resilience.Policy{}),
	),
})
```

The acceptance suite stands up an upstream that fails twice and then succeeds,
and asserts the call returns the successful result — and, per the R1/R2 lesson,
**fails if the retry middleware is removed from the chain**. A wiring line no
test can miss is the only kind worth writing.

## 7. Acceptance criteria

1. `resilience/go.mod` has zero `require` directives; the module builds and
   tests standalone under `GOWORK=off`.
2. Every exported constructor returns the unnamed
   `func(http.RoundTripper) http.RoundTripper`, and a compile-time check
   demonstrates the result is assignable to `httpclient.Middleware` (the check
   lives in the exemplar, which may import both).
3. A request with a non-replayable body (`Body != nil`, `GetBody == nil`) is
   **not** retried, and the body actually sent on the single attempt is intact.
4. A retryable request replays its body correctly: the server observes the same
   bytes on attempt 2 as on attempt 1.
5. `POST` is not retried by default; `GET` is; `Policy.RetryMethods` overrides
   both.
6. Every discarded response is drained — asserted by counting `StateNew` on an
   `httptest.Server` and requiring **one** connection across a multi-attempt
   retry, not one per attempt.
7. `Retry-After` on a 429 or 503 overrides the computed backoff; a malformed
   value falls back to the policy.
8. A context deadline shorter than the computed delay ends the loop immediately
   rather than sleeping; a context cancelled mid-sleep aborts the sleep.
9. `Exponential` grows and saturates at `max`; `Jitter` stays within `[0, d]` of
   its base policy and is not constant across attempts.
10. 100% statement coverage on `resilience`; mutation score at or above the 0.85
    floor, with every survivor killed or justified in
    `docs/mutation-survivors.md`.

## 8. Testing and CI

`scripts/ci.sh` and `scripts/mutation.sh` derive their module lists from the
`go.mod` files on disk, so `resilience` is gated with no edit. R2 was the first
milestone to collect on that and it worked; verify rather than assume.

**`-race -count=10` is required for this module.** It has a timer and a retry
loop on the request path, and `Jitter` draws from a random source. Use
`math/rand/v2`, whose top-level functions are goroutine-safe and need no
seeding — `math/rand`'s global source would be a shared mutable dependency on
every request.

**Determinism.** `Jitter` must be testable without flakiness. `Policy` takes no
clock or RNG seam in R3; instead `Jitter`'s contract is stated as a *range*
(criterion 9) and tested as one, over enough draws to make a constant-return
implementation fail. If that proves too weak in implementation, adding a seam is
preferable to weakening the assertion.

**A known mutation blind spot, carried from R2.** `go-mutesting` does not mutate
struct-literal field assignments, which is the shape of `Policy` defaulting.
Cover the defaults with a distinct value per field, as `httpclient`'s
`TestEveryOptionLandsOnItsOwnDestination` does.

## 9. Open questions

Two, both bounded and answerable during implementation:

1. **How much of a discarded body to drain.** Draining to EOF returns the
   connection; draining an unbounded error page from a hostile upstream is a
   denial of service against the client. A cap (e.g. 64KB, then close without
   reuse) is the usual answer. The cap belongs in the implementation with a test
   showing both branches, not as a `Policy` knob.
2. **Whether `Timeout` should exist in R3 at all.** It is three lines over
   `context.WithTimeout` and a caller can write it. It is included because
   per-attempt timeouts are what make retry safe against a hanging dependency,
   and the composition-order point in §4 is worth documenting. If the reviewer
   disagrees, dropping it costs nothing else.

## 10. Deviations from the parent spec

1. **Clients and resilience remain split.** The parent pairs them at R4; R2
   §1.1 split them and R3 keeps the split, shipping resilience alone.
2. **No circuit breaker in the resilience milestone.** The parent implies one
   module covering resilience; §1.1 defers the breaker to its own milestone.
3. **`POST` is not retried by default.** The parent does not specify. Recorded
   because a caller expecting "retry" to mean "retry everything" will be
   surprised, and the surprise is deliberate.
