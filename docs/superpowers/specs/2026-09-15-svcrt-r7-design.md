# svcrt R7 — circuit breaker

**Status:** design for review
**Date:** 2026-09-15
**Scope:** milestone R7 of the svcgen/svcrt spec v0.2
**Predecessor:** [R6 design](2026-09-14-svcrt-r6-design.md) — `kit`
**Deferred from:** R2 §1.1, re-deferred at R3, R5 and R6

---

## 1. Why it waited, and why it stops waiting

R2 deferred the breaker with reasoning that has held for four milestones:

> A circuit breaker is a concurrent state machine — closed/open/half-open, a
> failure window, a cooldown clock, and controlled probing while open. It is the
> hardest thing in that pairing by a wide margin, and the piece most likely to
> need the `-race -count=10` gate that caught R1's dropped-fatal data race behind
> 100% coverage and a 0.97 mutation score.

That is still true, which is why it gets a milestone to itself rather than being
appended to one. It is now the last obvious gap in the runtime: a service can
retry, time out, trace, measure and log, but cannot stop hammering a dependency
that is plainly dead.

`Retry` alone makes that worse. Three attempts against a down upstream is three
times the load at exactly the moment it can least afford it, and every caller
doing the same is how a struggling dependency is held under.

## 2. Surface

```go
func Breaker(p BreakerPolicy) func(http.RoundTripper) http.RoundTripper

type BreakerPolicy struct {
	FailureThreshold int           // 0 -> 5
	Cooldown         time.Duration // 0 -> 30s
	TripIf           func(*http.Response, error) bool // nil -> §4
}

var ErrOpen = errors.New("resilience: circuit breaker is open")
```

`Breaker` returns the bare unnamed func type, like every other constructor in
this module, so it stays assignable to `httpclient.Middleware` without
`resilience` importing anything — `docs/conventions.md` §2.

`BreakerPolicy` rather than reusing `Policy`: they share no fields, and a single
struct whose halves apply to different middlewares would be the kind of option
that silently does nothing (the defect `kit` was built to avoid).

## 3. One breaker per middleware, and no map

State lives on the value `Breaker` returns. There is no keying by host.

This repo's established pattern is a dedicated client per upstream —
`examples/orders` builds `NewPricingClient(cfg.PricingURL, kit.NewClient(...))`
— so a breaker per client *is* a breaker per upstream. Keying by host would add
a concurrent map to the request path, an eviction policy, and a growth surface
driven by whatever hosts the caller dials, to solve a problem the repo's own
usage does not have.

**The limitation, stated so nobody discovers it in production:** a client shared
across several upstreams gets one breaker for all of them, and one dead host
will open the circuit for the healthy ones. `Breaker`'s doc comment says this in
those words. The fix is a client per upstream, which is the recommendation
anyway.

## 4. What trips it is NOT what `Retry` retries

```go
func defaultTripIf(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return resp.StatusCode >= 500
}
```

Set against `defaultRetryIf`, which retries transport errors, 429, 502, 503 and
504:

| | `Retry` retries | `Breaker` trips |
|---|---|---|
| transport error | yes | yes |
| 429 Too Many Requests | **yes** | **no** |
| 500 Internal Server Error | **no** | **yes** |
| 502 / 503 / 504 | yes | yes |

Both differences are deliberate and neither is obvious.

**429 must not trip the breaker.** It is flow control, not a broken dependency:
the upstream is up, responding, and telling you the rate is too high. Opening a
circuit on it converts throttling into a self-inflicted outage, and does it at
precisely the moment the upstream is asking for less load rather than none.
`Retry` honours `Retry-After` on a 429 (R3 §5.1); the breaker stays closed and
lets it.

**500 must trip it even though `Retry` will not retry it.** R3's reasoning for
not retrying a 500 is that it usually repeats — which is the same fact that
makes it exactly what a breaker is for. Retrying a deterministic failure wastes
one call; continuing to send traffic into it for minutes wastes all of them.

A caller who disagrees sets `TripIf`. The two predicates are separate fields on
separate policies precisely so they can disagree.

## 5. The state machine

```
            failures >= threshold
   CLOSED ────────────────────────> OPEN
     ^                                │
     │                                │ cooldown elapsed
     │ probe succeeds                 v
     └───────────────────────────  HALF-OPEN ──┐
                                        ^      │ probe fails
                                        └──────┘   (cooldown restarts)
```

- **Closed.** Requests pass. A tripping result increments a consecutive-failure
  count; **any** non-tripping result resets it to zero.
- **Open.** Requests are rejected immediately with `ErrOpen`, without calling
  the next transport. After `Cooldown` since the trip, the next request becomes
  a probe.
- **Half-open.** Exactly **one** request in flight. It succeeds → closed, count
  zeroed. It fails → open, cooldown restarts. Concurrent requests arriving while
  a probe is outstanding are rejected with `ErrOpen` rather than queued or
  admitted — admitting them would send a burst at an upstream that has just
  demonstrated it is unwell, which is the failure mode the breaker exists to
  prevent.

### 5.1 Consecutive failures, and what that cannot see

Any success resets the count, so a **steady error rate never trips the
breaker**: an upstream failing 30% of calls forever will, with a threshold of
5, almost never produce five failures in a row. This is a real limitation of
consecutive counting and it is chosen knowingly. The state machine is this
milestone's hard problem; adding a time-bucketed rolling window would add a
second clock interaction and a ring buffer to the same milestone.

`BreakerPolicy` has room for a window-based field later, and `TripIf` already
lets a caller supply their own signal. The doc comment states the limitation
rather than letting it be inferred.

## 6. Composition

```go
kit.NewClient -> Breaker -> Retry -> telemetry.Client -> caller -> transport
```

**Outside `Retry`**, so the breaker counts logical calls rather than attempts. A
request that succeeds on attempt 2 is a success; one that exhausts its retries
is a single failure. Inside `Retry`, a three-attempt burst against a briefly
flaky upstream would trip a threshold-3 breaker even though the call ultimately
succeeded.

It also means an open circuit short-circuits the **whole** retry loop rather
than letting it spin through attempts that are all going to be rejected.

R6 established that a wrong order here is silent, so this ordering gets the same
treatment as the others: a test that fails when it is inverted.

## 7. The clock

The breaker's transitions are time-based, so `BreakerPolicy` carries an
unexported `now func() time.Time` defaulting to `time.Now`, set by internal
tests. `resilience` already has `retry_internal_test.go` and
`drain_internal_test.go`, so an internal test is an established pattern here.

This differs from R3's choice to give `Jitter` no clock seam and state its
contract as a range. That worked because jitter's contract *is* a range. A
cooldown boundary is exact — "the probe is admitted at T+cooldown and not
before" — and testing it against the wall clock means either sleeping for real
(slow, and flaky under `-count=10`) or asserting something weaker than the
contract.

## 8. Concurrency

This is the milestone's real risk. The state is shared and mutable and sits on
the request path, which is the profile that produced R1's dropped-fatal race
behind 100% coverage and a 0.97 mutation score.

- All state transitions happen under one mutex. The mutex is **not** held across
  `next.RoundTrip` — holding it there would serialise every request through the
  breaker and turn it into a global lock on the client.
- The half-open probe is claimed by a compare-and-set inside the mutex, so
  exactly one goroutine can hold it, and it is released in a `defer`.
- `resilience` is already in `COUNT_MODULES`, so `-race -count=10` applies with
  no change. Verify that rather than assume it — R3 shipped a module onto the
  request path and found afterwards the gate had never covered it.

A test must run concurrent requests through a tripping breaker and assert both
that the state machine ends up consistent and that no more than one probe was
admitted per cooldown.

## 9. Acceptance criteria

1. `resilience` still has **zero** require directives and imports only stdlib.
2. `Breaker` returns the bare unnamed `func(http.RoundTripper) http.RoundTripper`;
   assignability to `httpclient.Middleware` asserted in the exemplar.
3. Closed → open after exactly `FailureThreshold` consecutive tripping results,
   and **not** at threshold−1.
4. Any non-tripping result resets the count: threshold−1 failures, one success,
   then threshold−1 failures leaves the breaker closed.
5. Open rejects with `ErrOpen`, matched by `errors.Is`, **without calling the
   next transport** — asserted by a transport that fails the test if invoked.
6. After `Cooldown`, exactly one probe is admitted; a concurrent second request
   during the probe gets `ErrOpen`.
7. Probe succeeds → closed and the failure count is zero. Probe fails → open
   with the cooldown restarted, asserted by a second probe being refused before
   another full cooldown.
8. A 429 does **not** trip the breaker; a 500 does. Both asserted directly,
   since both differ from `RetryIf`.
9. `TripIf` overrides the default, proven with a predicate that trips on a
   status the default ignores.
10. The cooldown boundary is exact: no probe at `T+Cooldown−1ns`, a probe at
    `T+Cooldown`.
11. `Breaker` composes outside `Retry` in `kit`. A test fails if inverted.
12. `-race -count=10` passes, including a test driving concurrent requests
    through a trip and a probe.
13. 100% statement coverage on `resilience`; mutation at or above 0.85, every
    survivor killed or justified with **executed** evidence.
14. `./scripts/ci.sh` exits 0 across all 11 modules.

## 10. `kit` integration, and the one negative flag

`kit.ClientOptions` gains `Breaker resilience.BreakerPolicy`, and the breaker is
**on by default** — a client that retries but never gives up is not the
"resilient client" `kit` promises.

That creates the one problem `kit` has so far avoided. Every other embedded
option's zero value means "sensible defaults", and there is no zero value that
means "no breaker at all". So `ClientOptions` also gains `DisableBreaker bool`.

A negative boolean is normally a smell, and it is being accepted here with the
reason recorded: the alternative is a pointer field whose nil means *enabled*,
which inverts Go's usual reading of a nil option and would be the more
surprising of the two. `kit` §5's rule that it declares no defaults of its own
still holds — the threshold and cooldown remain `resilience`'s.

## 11. Out of scope

Rolling-window or ratio-based tripping (§5.1). Per-host keying (§3). Breakers
for anything other than `http.RoundTripper`. Metrics or spans for state
transitions — `resilience` has zero dependencies and must keep them; a service
wanting breaker telemetry can observe `ErrOpen` at the call site.
