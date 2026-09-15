# svcrt R6 — `kit`

**Status:** design for review
**Date:** 2026-09-14
**Scope:** milestone R6 of the svcgen/svcrt spec v0.2
**Predecessor:** [R5 design](2026-09-14-svcrt-r5-design.md) — `telemetry`

---

## 1. Why this exists

Every production service wants the same client: retried, traced, measured. R5
shipped the pieces and proved, with tests, that assembling them wrongly fails
**silently**. There are three such points, and a service can get all three
wrong while every test it owns still passes:

| composition | what a wrong order costs | what complains |
|---|---|---|
| `telemetry.Client` outside `resilience.Retry` | three attempts collapse into one span; retries become invisible | nothing |
| `telemetry.Server` inside `httpserver.AccessLog` | the access line loses **both** `trace_id` and its route | nothing |
| a logger built without `telemetry.LogExtractor` | spans and metrics are correct; logs carry no `trace_id` | nothing |

The third is the one most likely to be missed, because nothing about building a
logger suggests it has anything to do with tracing — R4 deliberately made
`logging` know nothing about spans. It is also the one that makes the other two
useless: a trace you cannot pivot to from a log line is a trace you will not
find during an incident.

`kit` encodes all three orderings once, in tested functions.

### 1.1 What it is not

It is **not** a facade, and not a layer anyone must go through. It composes
modules that remain independently usable, exposes their types directly (§3),
and is worth abandoning the moment its defaults stop fitting. A service that
outgrows `kit` deletes one import and writes the four lines itself — which is
the property that makes adopting it safe.

## 2. `kit` is the second module allowed to import, and the first to import siblings

Today **no** svcrt library module imports another. `kit` imports four:
`httpclient`, `resilience`, `telemetry`, `logging`. It deliberately does not
import `httpserver` (§4.2).

R5 set the precedent: the zero-requires rule narrowed to core modules because
`telemetry`'s whole job required OTel. The same argument applies with the same
shape — a module whose entire purpose is composition cannot compose without
importing the things it composes. `conventions.md` §11 gains a second named
exception.

The property being protected was never "no module ever depends on anything". It
was **"adopting a core module costs you nothing"**, and that survives intact:
`kit` is opt-in, nothing imports it, and a service that ignores it inherits
exactly what it did before. The coupling is paid only by callers who asked for
it.

**What it does cost, stated plainly.** A service on `kit` cannot upgrade
`resilience` independently of `kit`; it upgrades `kit`, which pins the set. That
is the actual trade and it is the reason `kit` must stay thin enough to leave.

## 3. Options embed sibling types rather than mirroring them

```go
type ClientOptions struct {
	HTTP      httpclient.Options
	Retry     resilience.Policy
	Telemetry telemetry.Options
}

type LoggerOptions struct {
	Logging logging.Options
	Baggage []string
}
```

Mirroring would re-declare roughly twenty-four fields across four types, every
one of which drifts the first time a sibling gains an option. Embedding costs
nothing `kit` has not already paid — it imports these modules — and inherits a
property the repo already relies on: **every one of these zero values already
means "defaults"**. `ClientOptions{}` is therefore a complete production stack,
with no defaulting logic of `kit`'s own to get wrong.

It also keeps the escape hatch honest. A caller reading `kit`'s API sees the
real types and can move to them directly.

## 4. Surface

```go
func NewClient(o ClientOptions) *http.Client
func NewLogger(w io.Writer, o LoggerOptions) *slog.Logger
```

### 4.1 `NewClient` — and what happens to caller middleware

```
Retry  ->  telemetry.Client  ->  o.HTTP.Middleware  ->  transport
```

`httpclient.Options.Middleware` collides with the chain `kit` builds. Silently
discarding it would be exactly the failure this module exists to prevent, so it
is **chained innermost** — closest to the wire.

Innermost rather than outermost is a real decision. Caller middleware is
typically per-request work that must happen on **each attempt**: attaching a
fresh auth token, signing a request, adding a tenant header. Outside `Retry` it
would run once and be replayed stale on attempts two and three. Inside
`telemetry.Client` it is also inside the span, so the work it does is attributed
to the attempt that did it.

### 4.2 The server composition is deliberately NOT here

`kit` ships no server-side helper, and does not import `httpserver`.

The asymmetry is the point. A service creates **one** handler chain, in one
visible place, usually in the same file as the rest of its wiring. It creates a
client **per upstream**, and each one is a fresh opportunity to compose wrongly
— which is where a helper earns its keep. Shipping a one-line wrapper for the
singular case would add a sibling import for very little.

**The residual risk, stated rather than elided.** The server inversion is the
most damaging of the three in §1: composing `telemetry.Server` inside
`httpserver.AccessLog` costs the access line its route *as well as* its
`trace_id`, because `Server` rebinds the request and `ServeMux` records the
matched pattern on the rebound instance. Nothing in `kit` prevents it. It is
guarded today only by `examples/orders`' `TestAcceptanceLogLineCarriesRouteAndStatus`,
which protects the exemplar and no one else's service.

`telemetry.Server`'s doc comment carries the ordering requirement and the
mechanism. That is the whole mitigation, and it is a weaker one than a tested
function — a deliberate trade, revisitable if the failure shows up in practice.

### 4.3 `NewLogger` — the reason this module is not client-only

```go
logging.New(w, o.Logging, telemetry.LogExtractor(o.Baggage...))
```

Four arguments' worth of wiring, and omitting the extractor is undetectable by
any test the service owns. `o.Baggage` is the allowlist R5 made opt-in because
baggage is attacker-controllable; `kit` does not widen it, and the zero value
emits `trace_id` and `span_id` only.

## 5. The defaults, and where they come from

`kit` declares **no default values of its own**. Every default is the sibling
module's zero value, already documented and already tested there:

| | zero value means |
|---|---|
| `httpclient.Options` | tuned transport, `MaxIdleConnsPerHost: 100`, no blanket timeout |
| `resilience.Policy` | 3 attempts, jittered exponential 100ms-2s, idempotent methods only |
| `telemetry.Options` | W3C TraceContext + Baggage, global tracer and meter providers |
| `logging.Options` | JSON at `slog.LevelInfo` |

This is deliberate. A default declared in `kit` would be a second source of
truth that drifts from the module that owns the behaviour, and would need its
own tests to prove it matches. Having none makes `kit` a composition and
nothing else.

## 6. Acceptance criteria

1. `kit/go.mod` requires the five svcrt modules and, transitively, OTel. It is
   the second entry in `DEP_EXEMPT`, with its own justification.
2. Every core module still has zero requires and still imports no sibling —
   `kit` must not become a reason for anything to start.
3. `NewClient` composes `Retry` outside `telemetry.Client`. **A test fails if
   the order is inverted**, asserting one span per attempt rather than one per
   call.
4. `o.HTTP.Middleware` is chained innermost and actually runs — asserted by a
   middleware that observes it ran once per attempt, not once per call.
5. `NewLogger` produces a logger whose records carry `trace_id` inside a span
   and **no** `trace_id` outside one.
6. `LoggerOptions.Baggage` forwards only allowlisted members.
7. `ClientOptions{}` and `LoggerOptions{}` — both zero values — produce a
   working stack, asserted end to end with **no OTel SDK**.
8. `kit` does **not** import `httpserver`, asserted structurally (§4.2).
9. Each embedded option field reaches its destination, one distinct observable
    value per field (the struct-literal mutation blind spot, carried from R2).
10. 100% statement coverage; mutation at or above 0.85, survivors killed or
    justified with **executed** evidence.
11. `conventions.md` §11 amended in place to name `kit` as the second
    exception, with the sibling-import distinction stated.
12. `./scripts/ci.sh` exits 0 across all 11 modules.

## 7. Testing notes

The two ordering criteria (3, and the extractor half of 5) are this
milestone's entire reason to exist, and each must be proven by **inverting the
composition and watching a named test fail**. R5 found four tests that could not
fail, every one by running a mutation rather than reading; a composition module
whose ordering tests are vacuous would be worse than no module, because it would
launder a wrong order behind a tested-looking API.

`examples/orders` migrates to `kit` and must keep passing **unmodified
assertions**. Its current hand-wired stack is the thing `kit` is extracted from,
so a changed assertion means `kit` is not equivalent to what it replaces.

## 8. Out of scope

**Any server-side helper** — not a constructor wrapping `httpserver.New`, and
not a handler wrapper either (§4.2). Non-HTTP
composition. Any default not owned by a sibling module (§5). Circuit breaking,
still deferred since R2.
