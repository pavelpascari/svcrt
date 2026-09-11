# svcrt R0 — `contract`, `config`, `logging`

**Status:** approved design, implemented at R0 with the corrections noted below
**Date:** 2026-09-10
**Scope:** milestone R0 of the svcgen/svcrt spec v0.2
**Parent spec:** svcgen/svcrt v0.2 (§7, §9, §12)

### Corrections applied during R0

Implementation contradicted this document in four places. Each has been
amended **in place** in the section named, with the correction marked. This
document remains the binding authority; where it once disagreed with the
code, the code was right and the text was stale.

| Where | What changed |
|---|---|
| §5.3 | `default:` is **not** the only way to make a field optional — a pointer type is optional by virtue of being a pointer. Plus the pointer-scalar / pointer-block default asymmetry. |
| §5.4 | A nested struct with **no** `envPrefix` tag is a `KindSchema` violation, not a flat embed. `envPrefix:""` is the flat embed. |
| §5.6 | `Validate()` runs only when stages 1–3 produced no violations, so the worked example mixing `KindValidate` with `KindRequired`/`KindDecode` was unproducible. Replaced with the reachable form. |
| §6.6, §7.2, §7.3 | Cosmetic: the logged route is `GET /orders/{id}` (`r.Pattern` includes the method), and the acceptance test file is `server_test.go`. |

---

## 1. Scope and rationale

R0 delivers three modules — `contract`, `config`, `logging` — plus an
`examples/orders` service that composes them.

The parent spec sequences the runtime before the generator (§12) so that P2
("every module is useful with the generator absent") is proven by construction
rather than by assertion. R0 is the first slice of that: three modules with no
dependencies on each other beyond `contract`, and no dependency on anything
outside the standard library.

R0 is specified alone rather than bundled with R1 because six modules exceed
what a single implementation plan handles well. R1 (`health`, `lifecycle`,
`httpserver`) gets its own spec.

### 1.1 Out of scope

Everything in R1–R4 and G0–G5. Specifically not here: HTTP server helpers,
health probes, lifecycle sequencing, OpenTelemetry, clients, resilience, and
every part of the generator.

---

## 2. Cross-cutting decision: codes, not prose

**The wire carries codes and language-neutral data. It never carries display
prose. Translation is the client's concern, and neither svcgen nor svcrt takes
a position on it.**

This is a new constraint, decided during this design session, and it
generalizes past errors to any client-facing value that would need
translation — enums, statuses, labels. It should be added to the parent spec's
§1.2 non-goals.

Two consequences that bind R0:

1. **No generated message templates.** An earlier proposal had the generator
   emit an English sentence per error code from its doc comment. That is the
   generator taking a position on language, and it is rejected.

2. **Error params are load-bearing.** A rendered sentence is unlocalizable
   downstream because its values are welded into the prose. Carrying the code
   plus its scalar substitutions is what lets any client render any language:

   ```json
   {"error": {"code": "invalid_quantity", "params": {"max": 100, "got": 5000}}}
   ```

   Without params, a coarse code cannot quote a limit or a received value, and
   the taxonomy has to explode into `quantity_above_100`, `quantity_above_500`
   to say anything useful.

The envelope itself is a G1 concern. What binds R0 is that `contract.Coded`
must accommodate params, because `contract` ships now and freezes at G2 (D3).

---

## 3. Repository layout

```
svcrt/
  go.work                    use ./contract ./config ./logging ./examples/orders
  contract/  go.mod          go 1.22, zero requires, forever
  config/    go.mod          go 1.25, zero requires
  logging/   go.mod          go 1.25, zero requires
  examples/orders/ go.mod    requires all three
  docs/superpowers/specs/
  scripts/release.sh
```

Three modules from day one, per parent §4.2. Starting single-module and
splitting at R1 was considered and rejected: the split is a breaking
import-path change for anyone who adopted early, and §4.4's tagging cost
arrives at R1 regardless, so deferring buys weeks and pays interest.

**`contract` sits at a lower Go floor than the rest** (`go 1.22` vs `go 1.25`).
It is the one module every consumer is forced to take and the one module that
freezes, so its job is to never be the reason someone cannot upgrade or cannot
stay put. It needs `context` and generics and nothing else, so the low floor is
free. `config` and `logging` are opt-in and may track the toolchain.

---

## 4. `contract`

### 4.1 Surface

```go
package contract

import "context"

// APIV1 is a compile-time compatibility marker. Generated code references it.
// A breaking change to this module removes it, so skewed generated code fails
// to build with a clear message rather than misbehaving at runtime.
type APIV1 struct{}

// Coded is implemented by errors whose code is part of the API surface.
type Coded interface {
	error
	ErrorCode() string
}

// Detailed is implemented by coded errors that carry language-neutral values
// for a client to substitute when rendering a message. Values must be scalars.
type Detailed interface {
	Coded
	ErrorParams() map[string]any
}

// Handler is a decoded, typed call.
type Handler[Req, Res any] func(ctx context.Context, req Req) (Res, error)

// Middleware is the typed, allocation-free call-level seam.
type Middleware[Req, Res any] func(Handler[Req, Res]) Handler[Req, Res]

// Chain folds middlewares into one, applied left to right.
func Chain[Req, Res any](ms ...Middleware[Req, Res]) Middleware[Req, Res]
```

Imports: `context` only. No `net/http`, no `log/slog`, no `encoding/json`.

### 4.2 Type-budget accounting

Parent §7 targets three exported types. This is five named types, and the
deviation is deliberate and recorded here rather than papered over:

| Identifier | Concept | Justification |
|---|---|---|
| `APIV1` | version skew | parent §8.4 |
| `Coded` | error taxonomy | parent §8.1 |
| `Detailed` | error params | §2 of this document |
| `Handler` | call seam | parent §7.1; unusable without `Middleware` |
| `Middleware` | call seam | parent §7.1; unusable without `Handler` |

Three concepts, five identifiers. §7's count treated `Handler`/`Middleware` as
one seam, which is fair since neither is usable alone. `Detailed` is the single
genuine addition, and §7's "every addition is a design review" is satisfied by
this section.

### 4.3 Why `Detailed` is named rather than structural

The alternative is for generated code to assert structurally:

```go
if p, ok := err.(interface{ ErrorParams() map[string]any }); ok { ... }
```

That keeps `contract` at four identifiers, but a user who writes `ErrorParam()`
— singular — compiles cleanly and silently never emits params. A named
interface is discoverable in godoc and compile-checkable at the definition site
with `var _ contract.Detailed = QtyErr{}`. Silent-on-typo is the exact failure
class the parent spec's strict directive rules exist to prevent, so accepting
one more identifier is the cheaper trade.

`Detailed` is optional rather than folded into `Coded` as a second required
method: the majority of error types carry no params and should not be made to
write `func (e E) ErrorParams() map[string]any { return nil }`.

### 4.4 `Chain`

A function, not a type, so it does not consume type budget. It earns its place
because generated code must fold a `[]Middleware` and every user would
otherwise hand-write the identical loop.

```go
func Chain[Req, Res any](ms ...Middleware[Req, Res]) Middleware[Req, Res] {
	return func(next Handler[Req, Res]) Handler[Req, Res] {
		for i := len(ms) - 1; i >= 0; i-- {
			next = ms[i](next)
		}
		return next
	}
}
```

Left-to-right application: `Chain(a, b)` yields `a(b(next))`, so the first
argument is outermost. This matches `net/http` middleware convention.
`Chain()` with no arguments returns the identity middleware, so a caller can
fold an empty slice without a special case.

---

## 5. `config`

### 5.1 Surface

```go
package config

type Source func(key string) (value string, ok bool)

func Load[T any](opts ...Option) (T, error)

func WithSource(Source) Option   // default: OSEnv()
func WithPrefix(string) Option   // global prefix applied to every key

func OSEnv() Source
func Dotenv(path string) (Source, error)   // local dev only, parent §9
func Layer(srcs ...Source) Source          // first hit wins

type Secret string

type Error struct{ Violations []Violation }
type Violation struct {
	Env   string        // full env var name, "" for whole-struct violations
	Field string        // e.g. "AppConfig.DBURL"
	Kind  ViolationKind
	Err   error
}
type ViolationKind int
const (
	KindSchema   ViolationKind = iota // programmer error: bad tag, bad type
	KindRequired                      // operator error: not set
	KindDecode                        // operator error: unparseable
	KindValidate                      // operator error: Validate() rejected
)
```

`Load` is generic and returns a value rather than filling a caller's pointer.
This makes "decoded and validated once at construction, immutable after"
(parent §9) the natural shape of the API rather than a doc comment: there is no
handle to mutate, no reload, no watch.

### 5.2 `Source` as the single seam

One function type does three jobs:

- the production reader (`OSEnv()`),
- the test seam — tests pass a `Source` backed by a map, so no `t.Setenv`, no
  process-global mutation, and every test runs parallel,
- the dotenv layering point.

`Layer(OSEnv(), dot)` puts real environment above the file, so a stray `.env`
can never shadow a deployed value. This is how parent §9's "no file fallback in
production" is enforced by construction: `Dotenv` is something you must
explicitly name in `main`.

### 5.3 Required vs. optional

**A field is required unless something makes it optional.** Absence at boot is
a fatal error. Two things make a field optional:

1. `default:"..."`, which supplies a value when the variable is absent; and
2. **a pointer type**, whose nil zero value already means "not set".

> **Corrected during R0.** This section originally said `default:"..."` was
> the *only* way to make a field optional, which contradicted its own
> `Debug *bool` example below and failed three of the plan's own tests.
> Pointer-means-optional wins: it is the entire purpose of the tri-state
> affordance, and it makes the rule uniform with §5.4, where a `*Struct` is
> already an optional block. Pointer ⇒ optional, at both levels. The cost is
> that a pointer field can never be mandatory; use a value type to require
> one, which is the clearer expression anyway.

There is no `required` keyword. Making the safe case the default means it
cannot be forgotten; the widely-used inverse (`env:"X,required"`) has a failure
mode where omitting the keyword silently admits a zero value to production.

Pointers give tri-state where "unset" must be distinguishable from "zero":

```go
type AppConfig struct {
	Port    int           `env:"PORT" default:"8080"`   // optional
	DBURL   Secret        `env:"DATABASE_URL"`          // required
	Timeout time.Duration `env:"TIMEOUT" default:"5s"`  // optional
	Debug   *bool         `env:"DEBUG"`                 // tri-state
	OTel    OTelConfig    `envPrefix:"OTEL_"`           // nested
	TLS     *TLSConfig    `envPrefix:"TLS_"`            // optional block
}
```

**The two pointer levels differ in one way, deliberately.** A `default:` on a
pointer *scalar* materializes it: `Retries *int` with `default:"3"` is a
non-nil pointer to 3 when the variable is unset. A `default:` on a field
inside a pointer *struct block* does **not** materialize the block — see
§5.4's presence rule below. A pointer scalar's default is the value to use
when the operator said nothing; an optional block's defaults are the values to
use once the operator has opted the block in.

### 5.4 Nesting

- **Prefixes compose transitively.** `envPrefix:"OTEL_"` on a field whose own
  struct declares `envPrefix:"EXPORTER_"` internally yields
  `OTEL_EXPORTER_ENDPOINT`.
- **`envPrefix:""` — present but empty — adds no prefix.** This is how a
  module ships a config fragment that embeds flat.

  > **Corrected during R0.** This bullet originally read "a nested struct
  > with no `envPrefix` adds no prefix". A *missing* tag is a `KindSchema`
  > violation, consistent with §5.5: an untagged field is never a silent
  > anything. `reflect.StructTag.Lookup` distinguishes absent from
  > present-but-empty, so opting into a flat embed is explicit and a
  > forgotten tag still fails the boot.
- **A `*Struct` is an optional block.** If no variable in its subtree is set,
  it stays `nil` and its required fields are not enforced. If any variable in
  its subtree is set, the block is decoded and its required fields *are*
  enforced. This is the optional-TLS case, and it is the same tri-state rule as
  §5.3 applied one level up.

  **Presence is decided by `Source` hits only.** A `default:` tag on a field
  inside an optional block does *not* materialize the block — otherwise any
  block containing a defaulted field could never be absent, which defeats the
  feature. Defaults apply only once the block has been materialized by some
  other variable in its subtree.

**`WithPrefix` composes outermost.** The full key for a field is
`WithPrefix + <chain of envPrefix> + env`. `WithPrefix("APP_")` with
`envPrefix:"OTEL_"` and `env:"ENDPOINT"` yields `APP_OTEL_ENDPOINT`. No
separator is inserted; prefixes are concatenated verbatim, so they must carry
their own trailing underscore.

### 5.5 Untagged exported fields are fatal

An exported field with no `env` and no `envPrefix` tag is a `KindSchema`
violation, not a silent skip. Parent P5 forbids inferring conventions, and
parent §5.4 already makes a missing binding tag fatal on the generator side —
this is the same rule, so users learn one dialect rather than two. It
eliminates the "added a field, forgot the tag, it silently stayed zero" bug.

`env:"-"` opts out explicitly. Unexported fields are skipped without comment.

### 5.6 Decode pipeline

Four stages, separating two error audiences:

1. **Plan** — reflect over `T` once, producing a flat list of bindings: field
   index path, full env key, decoder, default. Failures are *programmer*
   errors (`KindSchema`): unsupported type, malformed tag, untagged field, two
   fields resolving to the same env key.
2. **Resolve** — call `Source(key)`; else use `default:`; else record
   `KindRequired`.
3. **Decode** — raw string to typed value (`KindDecode`).
4. **Validate** — `Validate() error` if `T` or `*T` implements it
   (`KindValidate`).

Stages accumulate *within their tier*, and the tiers gate each other:

- **Stage 1 gates everything.** A schema violation makes the type unloadable
  under any environment, so reporting missing values alongside it would be
  noise. If stage 1 reports anything, `Load` returns those violations alone.
- **Stages 2 and 3 accumulate together.** Every key is resolved and every
  present value decoded before `Load` returns, so an operator sees the whole
  list in one restart — parent §9's "fails fast with every violation reported
  at once, not one per restart."
- **Stage 4 runs only if stages 1–3 produced nothing.** Handing user code a
  half-decoded struct would make `Validate` reason about fields that were
  never populated, producing cascading nonsense on top of the real errors.

> **Corrected during R0.** The worked example below showed three problems,
> mixing a `KindValidate` line with `KindRequired` and `KindDecode` lines.
> Given the gating above, that combination is **unproducible** — a reader who
> tested it against the code would conclude the code was broken. It has been
> replaced with the reachable two-problem form, which is the project's actual
> golden file (`config/testdata/aggregated.golden`).

Violations are ordered by field declaration order, so output is stable and
golden-testable.

```
config: 2 problems
  DATABASE_URL (cfg.DBURL): required, not set
  PORT (cfg.Port): invalid int: parsing "abc": invalid syntax
```

A `KindValidate` failure appears alone, because it is only reachable when
nothing else failed:

```
config: 1 problem
  cfg: TLS_KEY required when TLS_CERT is set
```

`*Error` implements `Unwrap() []error` so `errors.Is`/`errors.As` reach
individual violations.

### 5.7 Supported types

- `string`, `bool`
- all sized and unsized `int`/`uint`/`float` kinds
- `time.Duration` — special-cased **ahead of** `TextUnmarshaler`, because it is
  an `int64` and would otherwise decode as a raw nanosecond count
- slices of any of the above, comma-separated
- any type implementing `encoding.TextUnmarshaler`
- pointer-to-scalar (tri-state) and pointer-to-struct (optional block)

The slice separator is a literal comma with **no escaping mechanism**, so a
value containing a comma cannot be expressed as a slice element. This is a
known limitation, accepted because the alternative is a quoting dialect. A
field needing commas in its values should be a `TextUnmarshaler` that parses
whatever format it likes.

Tag keys are separate struct tags, not a comma-joined option list:
`` `env:"PORT" default:"8080"` ``, not `` `env:"PORT,default=8080"` ``.
Separate keys keep each value unambiguous and avoid inventing an escaping rule
for defaults that contain commas.

**`""` decodes to an empty slice**, not a one-element slice containing `""`.
This is deliberately the same rule as parent §5.4's `csv=`.

**Duplicate env keys across two fields are fatal**, deliberately the same rule
as parent §5.4's "two fields binding the same source key."

**`url.URL` is not supported out of the box.** It implements the *binary*
unmarshaler pair, not `encoding.TextUnmarshaler`. This is a known sharp edge
and is expected to be the first "why doesn't this work" — it is left visible
rather than special-cased, because a one-line named type or a `url.Parse` in
`Validate()` is a small cost and we should feel the pain before growing the
type table. Revisit if it recurs.

### 5.8 `Secret`

```go
type Secret string

func (s Secret) String() string               { return "[REDACTED]" }
func (s Secret) LogValue() slog.Value         { return slog.StringValue("[REDACTED]") }
func (s Secret) MarshalText() ([]byte, error) { return []byte("[REDACTED]"), nil }
```

Reading the real value requires an explicit `string(s)` conversion. Accidental
exposure — a `%v`, a log line, a JSON dump of the config struct — redacts;
intentional use is a visible cast at the call site that a reviewer can see.

**Redaction lives in `config`, not `logging`,** because it belongs at the point
secrets are born. Implemented here it works through `fmt`, `encoding/json`, and
*any* `slog.Handler` — including `slog.JSONHandler` for users who never import
`svcrt/logging`. Implemented in the handler it would protect only our own
users.

`config` importing `log/slog` for `LogValue` is stdlib-only and does not
violate the zero-requires rule.

### 5.9 Reflection budget

All reflection is inside `Load`, on the startup path. Parent P4 permits this
explicitly.

There is **no per-type plan cache**. A package-level memo map is exactly the
mutable global state parent P3 bans, and `Load` runs once per process, so the
cache would buy nothing.

---

## 6. `logging`

### 6.1 Surface

```go
package logging

type Extractor func(context.Context) []slog.Attr

type Options struct {
	Level       slog.Leveler                          // default slog.LevelInfo
	AddSource   bool
	ReplaceAttr func(groups []string, a slog.Attr) slog.Attr
}

func New(w io.Writer, opts Options, ex ...Extractor) *slog.Logger
func NewHandler(next slog.Handler, ex ...Extractor) slog.Handler
func Middleware(l *slog.Logger) func(http.Handler) http.Handler

const (
	KeyTraceID = "trace_id"
	KeySpanID  = "span_id"
	KeyMethod  = "method"
	KeyRoute   = "route"
	KeyStatus  = "status"
	KeyCode    = "code"
	KeyDurMS   = "duration_ms"
)
```

`Level` is a `slog.Leveler`, not a `slog.Level`. That costs nothing and lets a
service that wants runtime level flipping pass a `*slog.LevelVar`, while parent
§9's "no runtime level flipping by default" holds because we never wire one up.

### 6.2 The extractor mechanism

Parent §9 lists `trace_id`/`span_id` among the well-known keys, but §9 gives
`logging` no dependency on `telemetry`. Correlation therefore cannot be a
direct call.

`Extractor` resolves this. `NewHandler` runs each registered extractor against
the record's `ctx` and appends the resulting attrs. `telemetry` (R3) will ship
a `LogExtractor()` that reads span context; `logging` never learns what a span
is, and the dependency arrow is never drawn.

```go
log := logging.New(os.Stdout, logging.Options{}, telemetry.LogExtractor())
```

### 6.3 What is deliberately absent

**No `Into(ctx)` / `From(ctx)`.** A logger-in-context API was considered and
rejected: it requires every call site to remember `logging.From(ctx)`, and a
plain `slog.Info` silently loses correlation. Extractors make correlation a
property of the handler, so no call site can lose it.

Business handlers get their logger by injection and their correlation from
extractors.

### 6.4 `WithGroup` correctness

This is the one subtle part of the module.

If a user calls `log.WithGroup("req")` and the handler then appends `trace_id`
via `Record.AddAttrs` at `Handle` time, `trace_id` lands *inside* the `req`
group, because attrs added after a group nest into it. Correlation keys must be
top-level or log pipelines cannot find them.

The fix: do not delegate `WithAttrs`/`WithGroup` to `next` eagerly. Record them
as an ordered op list; at `Handle` time apply extractor attrs to the root
handler first, then replay the recorded ops on top.

```go
func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if len(h.ops) == 0 { // fast path: no WithAttrs/WithGroup applied
		r = r.Clone()
		for _, e := range h.ex {
			r.AddAttrs(e(ctx)...)
		}
		return h.next.Handle(ctx, r)
	}
	n := h.next // slow path: extractor attrs at root, then replay
	for _, e := range h.ex {
		n = n.WithAttrs(e(ctx))
	}
	for _, op := range h.ops {
		n = op.apply(n)
	}
	return n.Handle(ctx, r)
}
```

`r.Clone()` on the fast path is required by the `slog.Handler` contract: a
handler must not mutate a record it did not create.

The fast path covers every service that never groups at the root, which is
expected to be nearly all of them.

### 6.5 Conformance testing

`testing/slogtest.TestHandler` drives a handler through the full `slog.Handler`
contract — groups, empty groups, inline attrs, zero times. Running it against
`NewHandler` wrapping a JSON handler is the cheapest proof the subtle parts are
right, and it is the main reason hand-writing a `slog.Handler` is acceptable
here at all.

This is a required test, not an optional one.

### 6.6 `Middleware`

Logs one line per request at completion: `method`, `route`, `status`,
`duration_ms`.

**Route, not path.** It uses `(*http.Request).Pattern` (Go 1.23+, populated by
`net/http.ServeMux`), so a log line carries `GET /orders/{id}` rather than
`/orders/8a3f...`. `r.Pattern` includes the method, and it is logged
verbatim. This bounds log cardinality the same way parent §8.3 bounds
metric cardinality.

`Pattern` is documented as empty when the request was not matched against a
pattern — the middleware mounted outside a `ServeMux`, or a 404. In that case
the `route` key is **omitted entirely**. It must not fall back to
`r.URL.Path`, which would reintroduce unbounded cardinality precisely on the
unmatched-URL paths that a scanner can generate at will.

**Status capture avoids the response-writer wrapper trap.** A naive
`http.ResponseWriter` wrapper silently drops `http.Flusher`, `http.Hijacker`,
and `io.ReaderFrom`, breaking SSE and connection upgrades. Instead the wrapper
implements a single method:

```go
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
```

`http.NewResponseController` walks the `Unwrap` chain, so all optional
interfaces remain reachable without enumerating their combinations.

A test asserting `Flusher` pass-through is required.

---

## 7. `examples/orders`

### 7.1 Purpose

Per parent §12's R2, the exemplar is where the runtime's APIs are proven
targetable by generated code. Starting it at R0 rather than R2 means
composition is tested from the first commit, and R2 becomes an extension rather
than a from-scratch effort.

R0's version also solves a specific problem: **`contract` has no consumer at
R0.** Its real consumer is generated code, which does not exist until G0.
Shipping a module that freezes at G2 with zero consumers for three milestones
is how the wrong shape gets frozen. The example supplies that consumer by hand.

### 7.2 Contents

```
examples/orders/
  go.mod          requires contract, config, logging
  main.go         Load -> logger -> mux -> ListenAndServe
  config.go       AppConfig: Secret, nested envPrefix block, required,
                  defaulted, *bool, optional *TLSConfig block
  orders.go       service + orderNotFound implementing contract.Coded
  middleware.go   contract.Middleware[GetOrderRequest, *Order]
  server_test.go  acceptance test
```

`middleware.go` and the `Coded` error exist as pressure on `contract`:

- The typed middleware proves the generic `Handler`/`Middleware` pair is
  actually writable before D3 freezes it at G2.
- The `Coded` error is mapped to a 404 by hand in `main.go`. This is
  intentionally ugly — it is precisely the boilerplate G1 deletes, and writing
  it makes G1's case concrete.

No `api/` package and no `svcgen:` directives at R0. Those arrive at R2.

### 7.3 Acceptance test

One test, composing all three modules:

1. `GET /orders/{id}` for a known id returns 200 and the expected body.
2. `GET /orders/{id}` for an unknown id returns 404 with body
   `{"error":{"code":"order_not_found"}}`.
3. The captured log output contains a line with `route=GET /orders/{id}` and
   the corresponding status.

**R0 is done when this test passes and the CI assertions in §8 hold.**

---

## 8. CI

### 8.1 P2 — each module is useful alone

```sh
for m in contract config logging; do
  (cd "$m" && GOWORK=off go vet ./... && GOWORK=off go test -race ./...)
done
```

`GOWORK=off` is the point. `go.work` masks version skew locally, so CI must run
without it or the guarantee is untested (parent §4.4).

### 8.2 Zero requires

```sh
for m in contract config logging; do
  n=$(cd "$m" && GOWORK=off go list -m all | wc -l)
  test "$n" -eq 1 || { echo "$m has dependencies"; exit 1; }
done
```

Exactly one line — the module itself.

### 8.3 The example

`examples/orders` runs *with* `go.work`, since it depends on three unpublished
modules. This is the one place the workspace is load-bearing rather than a
local convenience.

### 8.4 Release script

`scripts/release.sh` ships at R0, not R1.

Parent §4.4 budgets release tooling for R1, but three modules already means
three tag prefixes (`contract/v0.1.0`, `config/v0.1.0`, `logging/v0.1.0`) and
the exact skew problem §4.4 warns about. Writing a ~20-line script while there
are three modules is markedly easier than writing it while there are six, and
§4.4's stated point is not discovering this mid-milestone.

Scope: tag a module at a version, and verify that module builds and tests at
that tag with `GOWORK=off`.

---

## 9. Deviations from the parent spec

Recorded so they are decisions rather than drift.

| # | Parent | Deviation | Rationale |
|---|---|---|---|
| 1 | §7 "three exported types" | `contract` has five named types | §4.2 — three concepts; `Detailed` added by §2's i18n constraint |
| 2 | §1.2 non-goals | Add "i18n / localization" as a non-goal | §2 |
| 3 | §4.4 release tooling at R1 | Release script at R0 | §8.4 |
| 4 | §12 R0 has no acceptance criterion | R0 gated on `examples/orders` acceptance test | §7.3 |
| 5 | §9 `config` "validated at construction" | Validation is presence + parse only; semantic rules go in user `Validate()` | §5.6; constraint tags are a DSL that cannot express cross-field rules anyway |

---

## 10. Open questions

None blocking implementation.

Carried forward from the parent spec and **not** resolved here, because neither
affects R0:

- **Q1** (parent) — optional vs. required for *request field binding*. R0
  answers the analogous question for `config` (§5.3: required by default,
  `default:` or a pointer type opts out). Aligning the binding answer with it
  would give users one dialect instead of two, but that decision belongs to
  G0.
- **D3** (parent) — whether `contract` freezes at G2. R0 ships `contract`
  unfrozen; §7.2's hand-written middleware exists to generate evidence for that
  decision.
