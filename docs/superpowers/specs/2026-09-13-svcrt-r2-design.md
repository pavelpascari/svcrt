# svcrt R2 — `httpclient`

**Status:** approved design, ready for implementation planning
**Date:** 2026-09-13
**Scope:** milestone R2 of the svcgen/svcrt spec v0.2
**Parent spec:** svcgen/svcrt v0.2 (§9, §12)
**Predecessor:** [R1 design](2026-09-12-svcrt-r1-design.md) — `lifecycle`, `httpserver`

---

## 1. Scope and rationale

R2 delivers **one module**: `httpclient`. It is to `http.Client` what
`httpserver` is to `http.Server` — correct construction, a middleware seam, and
defaults that are hard to get wrong.

R0 gave a service its vocabulary, configuration and logs. R1 gave it a process
shape. R2 gives it a way to call something else, which is the first thing a
real service does after it can serve.

### 1.1 Why not `resilience` in the same milestone

The parent spec pairs clients and resilience at R4, and the pairing is natural:
a client with no retry is half a story. They are split here anyway.

A circuit breaker is a concurrent state machine — closed/open/half-open, a
failure window, a cooldown clock, and controlled probing while open. It is the
hardest thing in that pairing by a wide margin, and the piece most likely to
need the `-race -count=10` gate that caught R1's dropped-fatal data race behind
100% coverage and a 0.97 mutation score. Appending it to a milestone whose other
half is a 150-line constructor would give it the design attention of an
afterthought.

`resilience` therefore gets its own milestone. §4.2 fixes the seam it will plug
into, and that seam costs nothing now.

### 1.2 Out of scope

Response decoding, typed errors, retries and backoff, circuit breaking, service
discovery, connection pooling policy beyond the defaults in §3.2, and every part
of the generator.

Anything requiring `contract` is out **by construction**, not by preference:
`httpclient` has zero requires, and that includes intra-project ones (§2).

## 2. The zero-requires invariant, restated because it drives the design

No library module in this repo imports another. Not `httpserver` importing
`contract`, not `logging` importing anything. Every `go.mod` under a library
module has zero `require` directives, and CI proves each builds standalone with
`GOWORK=off`.

The consequence for R2 is concrete and shapes §4.2: `httpclient` cannot return
`contract.Coded` errors, and a future `resilience` cannot import `httpclient` to
define middleware against it.

The escape is the one this project already uses. `lc.OnDrain(health.Drain)`
couples lifecycle to health with no import, because the shared vocabulary is a
method value rather than a shared type. Here the shared vocabulary is **stdlib**:
`http.RoundTripper`. Two modules that both speak it compose without either
knowing the other exists.

## 3. Surface

### 3.1 `New` returns `*http.Client`

```go
func New(opts Options) *http.Client
```

Not a wrapper type. The module's value is a correctly-configured client, not a
new vocabulary, and returning the stdlib type means the result drops into every
API that expects one — SDKs, generated clients, anything — with no accessor and
nothing to unwrap.

`httpserver` returns a `*Server` wrapper because it owns `Start`/`Shutdown`. A
client owns no lifecycle, so there is nothing for a wrapper to hold. Idle-
connection cleanup is one line in the consumer's wiring (§5) rather than a type.

The cost, accepted: a caller can assign `c.Transport` afterwards and silently
discard the configured transport and its middleware. Documented on `New`. The
alternative — a wrapper hiding the field — buys that one protection at the price
of interop friction paid at every call site forever.

### 3.2 `Options` and the defaults that matter

```go
type Options struct {
	DialTimeout           time.Duration // <=0 -> 5s  (stdlib default is 30s)
	TLSHandshakeTimeout   time.Duration // <=0 -> 10s, matching http.DefaultTransport
	ResponseHeaderTimeout time.Duration // <=0 -> 10s  (stdlib leaves this unbounded)
	IdleConnTimeout       time.Duration // <=0 -> 90s, matching http.DefaultTransport
	ExpectContinueTimeout time.Duration // <=0 -> 1s,  matching http.DefaultTransport
	MaxIdleConns          int           // <=0 -> 100, matching http.DefaultTransport
	MaxIdleConnsPerHost   int           // <=0 -> 100  (stdlib effective default is 2)
	Timeout               time.Duration // 0 -> none; negative passes through unchanged; see 3.3
	Middleware            Middleware
}
```

Every field above takes its default for a non-positive value, not only zero: a
negative duration reaching `http.Transport` or `net.Dialer` unclamped means NO
deadline at all to those types -- the opposite of what a caller setting a
negative value could have intended. `Timeout` is the one exception, per §3.3:
it has no default, so a negative value is passed through to
`http.Client.Timeout` unchanged rather than clamped.

Values were read off `http.DefaultTransport` at go1.25 rather than chosen, so
that the only deviations are the three that are deliberate. Verified empirically,
not from memory:

```
DefaultMaxIdleConnsPerHost = 2
DefaultTransport: MaxIdleConns=100 MaxIdleConnsPerHost=0 ForceAttemptHTTP2=true
                  TLSHandshakeTimeout=10s IdleConnTimeout=1m30s
                  ExpectContinueTimeout=1s ResponseHeaderTimeout=0s
fresh &http.Transport{}: ForceAttemptHTTP2=false MaxIdleConnsPerHost=0
```

**`DialTimeout` is the first deliberate deviation.** `http.DefaultTransport` uses
30s; we use 5s. A TCP connect taking more than 5s means SYN retransmits — the
dependency is effectively down — so fail fast to free the goroutine and preserve
the caller's remaining deadline budget. A caller wanting the stdlib value sets
the `Options` field.

**`MaxIdleConnsPerHost` is the reason this module earns its place.**
`http.Transport` leaves it 0, which means the package default of **2** — and
`http.DefaultTransport` leaves it 0 as well, so the standard client has the same
ceiling. A service calling one dependency under any concurrency beyond two opens
a fresh connection per request — TCP plus TLS handshake — and returns it to a
pool that immediately discards it. It surfaces as latency and connection churn
that nothing in the application explains, and almost nobody sets it. Raising it
to match `MaxIdleConns` is most of this module's practical worth.

**`ResponseHeaderTimeout` is the other major deviation.** `DefaultTransport` leaves it
at zero, i.e. unbounded: a server that accepts a connection and never sends
headers hangs the caller until its context expires, and a caller with no
deadline hangs forever. §3.3 explains why this is the right timeout to default
and a blanket one is not.

### 3.2.1 Two silent losses that a fresh transport suffers from `DefaultTransport`

`http.Transport` enables HTTP/2 automatically only when `TLSClientConfig`,
`Dial` and `DialContext` are all nil. This module sets `DialContext` to
implement `DialTimeout` — so a fresh transport built the obvious way negotiates
**HTTP/1.1 only**, with no error and no log line. `http.DefaultTransport` avoids
this by setting `ForceAttemptHTTP2: true`; a fresh `&http.Transport{}` has it
`false`.

That is a silent protocol downgrade introduced by the act of configuring a
timeout, which makes it precisely the kind of defect this module exists to stop
its callers from writing. `New` sets `ForceAttemptHTTP2: true`.

It is asserted in §6, because a downgrade that produces no error is invisible to
every test that only checks the response.

A fresh `http.Transport` also has `Proxy: nil`, so `HTTP_PROXY`, `HTTPS_PROXY`,
and `NO_PROXY` environment variables are ignored completely. In an egress-
controlled network, this silently bypasses a security control or breaks every
request without error. This is the same failure mode as `ForceAttemptHTTP2`
above — a fresh transport loses `DefaultTransport`'s behavior, here its proxy
settings. `New` sets `Proxy: http.ProxyFromEnvironment`.

### 3.3 No blanket `Timeout` by default

`http.Client.Timeout` bounds the **whole exchange**, including reading the
response body. Defaulting it would bound the one thing that legitimately takes
arbitrary time: a streaming response, an SSE subscription, a long poll, a large
download. Those would fail at the default, intermittently, in a way that reads
as a network problem.

The hang worth defending against is different: a server that accepts the
connection and never sends response headers. `ResponseHeaderTimeout` catches
exactly that, and bounds nothing about the body.

So the default set is transport-level, `Timeout` stays opt-in, and per-request
deadlines belong on the context — which every caller already has and which
`http.Client` already honours.

This is a deliberate departure from "hard to get wrong": a caller who sets no
context deadline and reads a body from a slow server can still block for a long
time. The trade is taken knowingly, because the alternative breaks correct
programs silently and this one only fails to rescue careless ones.

### 3.4 Never touch the process globals

`New` always constructs a fresh `*http.Transport`. It never reads, copies from,
or mutates `http.DefaultTransport`, and never returns `http.DefaultClient`.

`http.DefaultTransport` is a process-wide singleton. A library that tunes it
re-tunes every other user of it in the same process, including ones that never
heard of this module; a library that uses `http.DefaultClient` inherits whatever
someone else did to it.

This is the client-side form of the `net/http/pprof` finding R1 recorded in
`conventions.md` §7 — importing that package mutates `http.DefaultServeMux` from
its `init()`, on any import. Same class of defect, same rule.

It is asserted, not merely stated: §6 requires a test that `http.DefaultTransport`
is unchanged across `New`, and that the returned client's transport is not it.

## 4. Middleware

### 4.1 Surface

```go
type Middleware func(http.RoundTripper) http.RoundTripper

func Chain(ms ...Middleware) Middleware
```

The exact client-side mirror of `httpserver.Middleware`. `Chain(a, b)` produces
`a(b(next))`, so `a` observes the request first and the response last, matching
both `net/http` convention and `httpserver.Chain`. `Chain()` with no arguments
returns the identity middleware, so a caller can fold an empty slice without a
special case.

The ordering and the empty case are pinned by tests in both modules, because two
seams that look symmetrical and behave differently are worse than two that look
different.

This is a **fourth** spelling of `Middleware` in the project. `conventions.md`
§2 records the existing three and must be amended in the same change, not left
to describe a world with three.

### 4.2 The seam `resilience` will use

A future `resilience` ships functions of this same shape without importing
`httpclient`, because `func(http.RoundTripper) http.RoundTripper` is expressible
from `net/http` alone:

```go
c := httpclient.New(httpclient.Options{
	Middleware: httpclient.Chain(
		resilience.Retry(policy),      // resilience imports net/http and nothing else
		resilience.CircuitBreaker(cb),
	),
})
```

No dependency arrow is drawn in either direction. R2 ships no resilience; it
fixes the shape so that milestone is additive.

## 5. The exemplar

`examples/orders` gains an outbound dependency: it calls a pricing service.

Unit tests can prove a transport is configured. Only an exemplar proves the
thing is usable, which is the standing reason exemplars exist here.

`appStackConfig` gains `PricingURL`. `main` reads it from config; the acceptance
suite stands up an `httptest.Server` as the upstream and points `buildStack` at
it. The client is built in `stack.go` alongside the rest of the wiring, per
`conventions.md` §8 — `main.go` holds `func main()` and nothing else, and the
mutation gate covers `stack.go` precisely because R1 found a real survivor
hiding in the file it used to exclude.

Idle-connection cleanup is wired in the consumer's code, in the established
idiom, with no coupling in either module:

```go
lc.Add("pricing-client", nil, func(context.Context) error {
	pricing.CloseIdleConnections()
	return nil
})
```

Per the M1 lesson from R1, the acceptance suite must **fail** when that wiring
is dropped. A wiring line no test can miss is the only kind worth writing; R1
shipped an `OnServeError: lc.Fatal` that could be deleted with every test green.

## 6. Acceptance criteria

1. `httpclient/go.mod` has zero `require` directives; the module builds and
   tests standalone under `GOWORK=off`.
2. `New` returns a client whose `Transport` is a fresh `*http.Transport`, not
   `http.DefaultTransport`.
3. `http.DefaultTransport`'s exported fields are unchanged across a `New` call.
4. Every `Options` field lands on its `http.Transport` (or `http.Client`)
   counterpart, asserted with a **distinct value per field**.
5. Zero-valued `Options` fields produce the documented defaults in §3.2.
6. `ForceAttemptHTTP2` is true on the constructed transport, and an end-to-end
   request against an `httptest.Server` started with `EnableHTTP2` reports
   `resp.Proto == "HTTP/2.0"`. Asserting the field alone is not enough: it
   proves the value was set, not that HTTP/2 actually negotiates once a custom
   `DialContext` is in play, which is the thing §3.2.1 is about.
7. `Chain(a, b)` yields `a(b(next))`; `Chain()` is the identity; middleware
   observes real round trips through an `httptest.Server`.
8. Context cancellation aborts an in-flight request.
9. `examples/orders` calls a pricing upstream through the client, and the
   acceptance suite fails if the client wiring or the `CloseIdleConnections`
   wiring is removed.
10. 100% statement coverage on `httpclient`; mutation score at or above the 0.85
    floor, with every survivor killed or justified in `docs/mutation-survivors.md`.

## 7. Testing and CI

`scripts/ci.sh` and `scripts/mutation.sh` both derive their module lists from
the `go.mod` files on disk, so `httpclient` is picked up with no edit. That is
why they were made derived, and R2 is the first milestone to actually collect
on it — verify it rather than assume it.

`-race -count=10` is not required for `httpclient`: it holds no mutable state
beyond what `http.Transport` already manages. It becomes required the moment
`resilience` lands, since a circuit breaker is shared mutable state on the
request path.

**Mutation blind spot, known in advance.** `go-mutesting` does not mutate
struct-literal field assignments, which is exactly the shape of `Options` →
`Transport` wiring. R1 found four `httpserver` wirings that survived deletion
undetected while the module still scored 1.000. Criterion 6.4's distinct-value
table test exists specifically to cover what mutation testing cannot see here,
and `docs/mutation-survivors.md` records the blind spot rather than letting a
1.000 read as total assurance.

## 8. Deviations from the parent spec

1. **Clients and resilience are split.** The parent pairs them at R4. §1.1 gives
   the reason: the circuit breaker deserves its own design pass.
2. **`New` returns `*http.Client`, not a module type.** The parent does not
   specify; this records the choice and its accepted cost (§3.1).
3. **No default request timeout.** A reader expecting "hard to get wrong" to
   mean "nothing can hang" should read §3.3, which takes the opposite trade
   deliberately.

## 9. Open questions

None blocking. Two noted for the milestone after:

- Whether `resilience` should also expose transport-agnostic combinators
  (`func(context.Context) error`) alongside the `RoundTripper` shape, for
  callers wrapping something that is not HTTP.
- Whether `telemetry` will make zero-requires a core-modules rule rather than a
  project-wide one. R2 does not force that decision; it is the first thing the
  telemetry milestone must settle.

## 10. Carried residual from R1

A full no-argument `./scripts/mutation.sh` has still never completed in one
invocation under the development harness — every attempt exceeded the per-call
ceiling, and all seven modules were verified individually instead. The gates are
evidenced; the script running end to end is not. It wants one run on a CI runner
without that ceiling, and R2 adding an eighth module makes it no cheaper to keep
deferring.
