# svcrt R5 — `telemetry`

**Status:** approved design, ready for implementation planning
**Date:** 2026-09-14
**Scope:** milestone R5 of the svcgen/svcrt spec v0.2
**Predecessor:** [R4 design](2026-09-13-svcrt-r4-design.md) — attribute ownership
**Depends on:** R4, which cleared `trace_id`/`span_id` out of `logging` so this
module can declare them.

---

## 1. Scope

R5 ships one module, `telemetry`, with three exported functions and two
constants. It is the module R4 was a prerequisite for: `logging` has an
`Extractor` seam and never learns what a span is, and this is the module that
knows.

```go
func LogExtractor(baggageKeys ...string) func(context.Context) []slog.Attr
func Server(o Options) func(http.Handler) http.Handler
func Client(o Options) func(http.RoundTripper) http.RoundTripper

type Options struct {
	Propagator propagation.TextMapPropagator // nil -> TraceContext + Baggage
}

const (
	KeyTraceID = "trace_id"
	KeySpanID  = "span_id"
)
```

`Server` extracts inbound trace context and starts a server span. `Client`
starts a client span and injects trace context outbound. `LogExtractor` reads
whatever span is in the context and turns it into log attributes. Together they
close the loop: a `traceparent` arriving on an inbound request appears in this
service's log lines and on its outbound calls.

### 1.1 Out of scope

**Metrics.** Not because of dependency weight — §2.1 measured that excluding
`otel/metric` saves nothing, it arrives as an indirect dependency either way —
but because metrics are a different instrument with a different lifecycle, and
bundling them means designing two things under one milestone's attention.

**SDK wiring.** `telemetry` depends on the OTel *API* only. Choosing an
exporter, sampler and resource is an application decision with an application's
lifetime, and a library that makes it takes the choice away from every consumer.
§4.2 shows what this costs and why it is affordable.

**Circuit breaking** (deferred since R2 §1.1), and every part of the generator.

## 2. The dependency decision

`telemetry` is the **first and only** module with `require` directives. The
zero-requires rule becomes a rule about *core* modules, with `telemetry` the
documented exception.

The rule was never "no dependencies ever". It was "a service should be able to
adopt `contract` or `logging` without inheriting anything". That holds exactly
as well when the module you inherit OTel from is the one whose entire job is
speaking OTel. A tracing library that refuses to depend on a tracing API would
have to reimplement W3C tracecontext parsing and span context plumbing — which
is not independence, it is a second, worse implementation of a standard.

`conventions.md` must be **amended in place** to say this, not annotated. The
core modules — `contract`, `config`, `logging`, `lifecycle`, `httpserver`,
`httpclient`, `resilience` — keep zero requires, and that is verified, not
assumed (§7.1).

### 2.1 What it costs, measured

```
require go.opentelemetry.io/otel       v1.46.0
require go.opentelemetry.io/otel/trace v1.46.0

indirect: otel/metric, auto/sdk, go-logr/logr, go-logr/stdr, cespare/xxhash/v2
```

Two direct requires, five indirect. Naming `otel/metric` directly would change
nothing — `otel` pulls it regardless — which is why §1.1's exclusion of metrics
is an API-surface argument and not a dependency-weight one.

## 3. Three functions, and why each is shaped the way it is

### 3.1 `LogExtractor` returns a bare func type

```go
func LogExtractor(baggageKeys ...string) func(context.Context) []slog.Attr
```

It must **not** return `logging.Extractor`. `telemetry` does not import
`logging` — that is the entire point of R4 — and even if it did, Go's
assignability rules would make a named return type the wrong choice. This is the
third application of the rule recorded in `conventions.md` §2 (R3 established
it, R4 preserved it): a module shipping a value for another module's named type
returns the bare unnamed type, because two different named types with the same
underlying type are not assignable.

**No valid span means no attributes.** Measured:

```
empty ctx -> IsValid()=false traceID="00000000000000000000000000000000"
                             spanID="0000000000000000"
```

An extractor that did not check `IsValid()` would stamp
`"trace_id":"00000000000000000000000000000000"` on every log line emitted
outside a request — every startup line, every shutdown line, every background
job. That is worse than absent: it is a well-formed field carrying a lie, and it
defeats grep-by-trace-id for the lines that do have one. `LogExtractor` returns
`nil` when the span context is invalid, which `logging`'s contract explicitly
permits ("Returning nil is fine and contributes nothing").

**It must not panic**, because `logging`'s `Extractor` doc says so in unusual
detail — nothing recovers, and a panicking extractor propagates into the request
being logged. `trace.SpanContextFromContext` does not panic on a context with no
span (measured above, it returns a zero-valued invalid context), and baggage
lookup of an absent key returns a zero-valued member. Neither path needs a
recover; the criterion in §7 is that this is *tested*, not assumed.

**Baggage is opt-in and empty by default.** `LogExtractor()` with no arguments
emits only `trace_id` and `span_id`. Baggage is the one place R5 accepts input
from an untrusted upstream: whatever a caller puts in a `baggage` header would
otherwise become a log attribute of unbounded name and cardinality. An explicit
allowlist — `LogExtractor("tenant_id")` — is the guard, and a variadic parameter
makes the safe default the one you get by writing nothing.

### 3.2 `Server` and `Client` return bare unnamed middleware types

```go
func Server(o Options) func(http.Handler) http.Handler
func Client(o Options) func(http.RoundTripper) http.RoundTripper
```

Same rule, same reason: these must be assignable to `httpserver.Middleware` and
`httpclient.Middleware` without `telemetry` importing either module.

`Client` **must not mutate the request it is given.** `http.RoundTripper`'s
contract says RoundTrip should not modify the request, and injecting a
`traceparent` header is a modification. Measured:

```
inject into req.Header directly -> caller's header count 0 -> 1
inject into Clone().Header      -> original="" clone="00-4bf92f35...-01"
```

A transport that writes headers into the caller's request corrupts a request the
caller may still hold — and with `resilience.Retry` in the chain, may replay.
This is the same shape of defect as R3 §3.1's body replay: invisible at the call
site, and the request still succeeds. `Client` clones.

`Options` exists as a struct, rather than functional options, because that is
what this repo already does — `httpclient.Options`, `resilience.Policy` — and
the zero value is the intended configuration.

## 4. Two globals, and why only one of them is acceptable

OTel has two process-wide registrations. `telemetry` treats them differently,
and the asymmetry is deliberate.

### 4.1 The propagator is owned, not read from the global

The default global propagator is a **no-op**. Measured:

```
default global propagator extract -> valid=false
default global propagator inject  -> headers=map[]
wired TraceContext extract        -> valid=true traceID="4bf92f35..." remote=true
```

So a service that wires `telemetry.Server(telemetry.Options{})` and never calls
`otel.SetTextMapPropagator` gets middleware that compiles, runs, allocates a
span, and propagates nothing. No error, no log line. That is precisely the
failure this project has hit in four consecutive milestones — a gate that looks
present and does not apply — and it is not a failure a library should hand its
users by default.

`telemetry` therefore owns its propagator, defaulting to
`TraceContext + Baggage`, and never reads `otel.GetTextMapPropagator()`. The
`Options.Propagator` field is the seam for anyone who needs B3 or a composite of
their own.

This also matches the rest of the repo, which has no globals: `logging.New`,
`httpclient.New` and `lifecycle` are all explicitly constructed.

### 4.2 The TracerProvider is read from the global, and its no-op default is correct

`telemetry` gets its tracer from `otel.Tracer(...)`, which reads the global
provider the application's SDK installs. With no SDK, that provider is a no-op —
and unlike the propagator, **this is a meaningful state rather than a broken
one**: "this service is not exporting traces" is a legitimate thing to be, and
it is the state every consumer starts in.

What makes it affordable is a property worth stating explicitly, because it is
not obvious. Measured:

```
no-SDK Start()            -> valid=false recording=false
no-SDK Start(remote ctx)  -> valid=true  traceID="4bf92f3577b34da6a3ce929d0e0e4736"
```

**Trace continuation works with the API alone.** A service with no SDK still
reads an inbound `traceparent`, carries that trace ID into its log lines, and
passes it to its own upstreams. It originates no traces of its own, but it does
not break the chain — a service in the middle stays correlated without adopting
the SDK at all.

The practical consequence for this milestone: **the entire acceptance suite
runs against the OTel API with no SDK dependency**, driven by an inbound
`traceparent` header. R5's tests need nothing the module does not already
require.

## 5. Composition

```go
// server: telemetry outside the access log, so the span exists when it logs
h = httpserver.Chain(
	telemetry.Server(telemetry.Options{}),
	httpserver.AccessLog(log),
)(mux)

// logger: the extractor is what carries trace_id into every line
log := logging.New(logging.Options{}, telemetry.LogExtractor())

// client: telemetry inside retry, so each attempt gets its own span
c := httpclient.New(httpclient.Options{
	Middleware: httpclient.Chain(
		resilience.Retry(resilience.Policy{}),
		telemetry.Client(telemetry.Options{}),
	),
})
```

**Order is load-bearing in both chains, and each has a test.**

`telemetry.Server` must be outside `httpserver.AccessLog`: the access log line
is emitted by the handler `AccessLog` wraps, so the span has to be in the
context by then. R4 confirmed the mechanism — `AccessLog` calls
`l.LogAttrs(r.Context(), ...)`, so the request context reaches the handler and
the extractor runs against it. Inverted, the access log line simply has no
`trace_id`, and nothing complains.

`telemetry.Client` inside `resilience.Retry` gives one span per attempt, which
is the useful arrangement: a retried call shows three spans, and the two that
failed are visible. Outside, three attempts collapse into one span and the
retries become invisible — the opposite of what you instrumented for.

## 6. `request_id` stays deferred, and this is the decision R4 asked for

R4 §9 left `request_id` to this milestone. **R5 does not add it**, and the
reason is R4's own lesson.

R4 deleted `logging.KeyTraceID` because it had zero producers — a constant is a
promise about something the module emits, and `logging` emitted nothing.
Nothing in R5 produces a `request_id` either. `trace_id` already carries
correlation, it is propagated by a standard, and every log line in a traced
request has one.

A `request_id` is a genuinely different thing only when a service needs a
short identifier to hand a human in an error response — which makes it a field
in `contract`'s error envelope, produced by whatever mints it, and R5 is not
that. Declaring the constant here would repeat exactly the mistake R4 spent a
milestone undoing.

## 7. Acceptance criteria

1. `telemetry/go.mod` requires `go.opentelemetry.io/otel` and
   `go.opentelemetry.io/otel/trace` and **nothing else** directly; the module
   builds and tests standalone under `GOWORK=off`.
2. Every other **library** module still has **zero** require directives,
   asserted by a check that fails if one gains a dependency — not by reading
   the files. The exemplars under `examples/` are exempt: they import
   `telemetry` and legitimately inherit OTel, which is the whole point of a
   module that is allowed to have dependencies.
3. `telemetry` imports neither `logging`, `httpserver`, `httpclient` nor
   `contract`, asserted structurally.
4. `LogExtractor()` returns the bare `func(context.Context) []slog.Attr`, and a
   compile-time check demonstrates assignability to `logging.Extractor`. Same
   for `Server`/`Client` against `httpserver.Middleware` and
   `httpclient.Middleware`. The checks live in the exemplar, which may import
   everything.
5. `LogExtractor` returns nil for a context with no span, and specifically does
   **not** emit all-zero `trace_id`/`span_id`.
6. `LogExtractor` does not panic on: a context with no span, a context with no
   baggage, an allowlisted key absent from baggage, and `context.Background()`.
7. `Client` does not mutate the request it is given — asserted by holding the
   original request and checking it has no `traceparent` after the round trip.
8. `Server` extracts an inbound `traceparent` and the resulting context carries
   a valid, remote span context with the inbound trace ID.
9. Baggage: `LogExtractor()` emits no baggage attributes even when baggage is
   present; `LogExtractor("tenant_id")` emits that one and **not** a
   non-allowlisted key present in the same baggage header.
10. Order: a test fails if `telemetry.Server` is composed inside
    `httpserver.AccessLog` rather than outside — the access-log line must carry
    `trace_id`.
11. End-to-end in `examples/orders`, with **no SDK**: an inbound request
    carrying a `traceparent` produces a log line with that trace ID, and the
    outbound upstream call carries a `traceparent` with the same trace ID.
12. `telemetry` is added to `COUNT_MODULES` in `scripts/lib.sh` and passes
    `-race -count=10`.
13. 100% statement coverage on `telemetry`; mutation score at or above the 0.85
    floor, with every survivor killed or justified in
    `docs/mutation-survivors.md`.
14. `conventions.md` amended in place: zero-requires stated as a core-modules
    rule with `telemetry` named as the exception, and no surviving sentence
    claiming the rule is project-wide.
15. `./scripts/ci.sh` exits 0 across all 10 modules; `go vet` clean; `gofmt`
    silent.

## 8. Testing notes

**The `-race -count=10` gate.** R3 shipped a module onto the request path and
discovered afterwards that the gate had never applied to it, because
`COUNT_MODULES` was missing an entry and the guard that checked it was
asymmetric — it caught a stale name but not a missing module. R3 fixed the
guard; criterion 12 is the first test of whether that fix works. Verify the
module is actually in the list rather than assuming the check catches it.

**Structural assertions over inspection.** Criteria 2 and 3 are the ones most
likely to rot into decoration. "`telemetry` does not import `logging`" is true
today and stays true only if something fails when it stops being true.

**The known mutation blind spot, carried from R2 and R3.** `go-mutesting` does
not mutate struct-literal field assignments, which is the shape of `Options`
defaulting. Cover the propagator default with a distinct observable value —
a custom propagator that writes a header W3C does not — so the default and the
override are distinguishable.

**No SDK anywhere in the test suite** (§4.2). If a test reaches for
`otel/sdk/trace`, that is a signal the behaviour under test is the SDK's rather
than this module's.

## 9. Deviations from the parent spec

1. **`telemetry` is API-only.** The parent implies a telemetry module without
   distinguishing API from SDK. §1.1 and §4.2 make the split and explain the
   cost.
2. **The zero-requires rule narrows to core modules.** Recorded because it is a
   project-wide invariant being changed, not a new module's local choice.
3. **No `request_id`.** The parent does not specify one; R4 §9 asked R5 to
   decide, and §6 decides against.
