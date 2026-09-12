# svcrt R1 — `lifecycle`, `httpserver`

**Status:** approved design, ready for implementation planning
**Date:** 2026-09-12
**Scope:** milestone R1 of the svcgen/svcrt spec v0.2
**Parent spec:** svcgen/svcrt v0.2 (§9, §12)
**Predecessor:** [R0 design](2026-09-10-svcrt-r0-design.md) — `contract`, `config`, `logging`

---

## 1. Scope and rationale

R1 delivers the **process shape**: ordered startup, graceful drain, and the HTTP
surfaces a service and a worker both need. Its acceptance criterion, from the
parent spec §12, is that at the end of R1 *a complete hand-written service is
possible with nothing else.*

R0 left an explicit marker for this milestone — `examples/orders/main.go` carries
a hand-rolled `http.Server` block and the comment `// R1 replaces this with
svcrt/httpserver and svcrt/lifecycle`. Deleting that block is R1's visible
outcome.

### 1.1 Two modules, not three

The parent spec §9 lists three: `health`, `lifecycle`, `httpserver`. This spec
ships **two**, folding `health` into `httpserver`.

The reasoning, recorded because it contradicts the parent:

`lifecycle` without health is a real standalone case — a CLI, a one-shot job, a
test harness. `health` without `httpserver` is not: a health probe is an
`http.Handler`, and Kubernetes reaches it over `httpGet`. Even a background
worker with no application HTTP surface must run a listener to answer its
probes, so the two always travel together.

Folding into `lifecycle` instead was considered and rejected: it would drag
`net/http` into the one module whose clearest standalone use is processes with
no HTTP at all, and it would couple probe semantics to a lifecycle a consumer
might not be using.

What the merge saves is real — parent §4.4 notes every module costs a tag
prefix and a release cadence forever, and that is a steep price for roughly 80
lines of atomic bools and handlers. What it preserves is the only thing in
`health` that justified a module at all: **the absence of an `AddLiveCheck`**
(§4.2).

What it does **not** change is the drain seam. `lifecycle` still cannot import
`httpserver`, so `main` still wires readiness to drain by hand (§3.5).

### 1.2 Out of scope

Metrics (`telemetry`, R3), clients and resilience (R4), and every part of the
generator. `AdminMux` is designed as the mount point R3's metrics handler will
attach to, but R1 ships no metrics.

---

## 2. Module boundaries

```
lifecycle/   go.mod   go 1.25, zero requires, no net/http
httpserver/  go.mod   go 1.25, zero requires
```

Both are standard-library only and import **neither each other nor** `contract`,
`config`, or `logging`. Parent §9 lists no dependency for either, and P2
requires each to be useful with every other `svcrt` module absent.

`lifecycle` additionally avoids `net/http` entirely. That is narrower than it
first appears — a worker on Kubernetes pulls `httpserver` anyway to serve its
probes — but it keeps `lifecycle` usable in CLIs, one-shot jobs, and tests, and
it keeps the dependency direction honest: `httpserver` composes *into*
`lifecycle`, never the reverse.

Two pieces of R0 infrastructure apply without change:

- `scripts/ci.sh` derives its module list from `*/go.mod`, so both modules are
  gated the moment they exist. This was fixed during R0's whole-branch review
  precisely because "R1 adds modules" was the stated risk.
- `scripts/mutation.sh` has a per-module floor table. Both inherit the 0.85
  default; `logging`'s 0.80 exception stays isolated.

---

## 3. `lifecycle`

### 3.1 Surface

```go
package lifecycle

// Ref identifies a registered component. Returned by Add, consumed by After.
type Ref struct{ /* opaque */ }

// StartFunc returns when the component is READY, not when it is finished.
type StartFunc func(context.Context) error
type StopFunc  func(context.Context) error

type Option func(*component)            // per-component
func After(refs ...Ref) Option
func StopTimeout(d time.Duration) Option

type Config struct {                    // per-lifecycle
    DrainDelay  time.Duration           // default 0
    StopTimeout time.Duration           // default 15s, per component
    Logger      *slog.Logger            // nil means silent
}

func New(cfg Config) *Lifecycle
func (l *Lifecycle) Add(name string, start StartFunc, stop StopFunc, opts ...Option) Ref
func (l *Lifecycle) OnDrain(func())
func (l *Lifecycle) Fatal(err error)
func (l *Lifecycle) Run(ctx context.Context) error

func SignalContext(parent context.Context) (context.Context, context.CancelFunc)
```

`Config` is a struct rather than functional options, per `docs/conventions.md`
§1: it configures a *value*, its knobs are plain data, and its zero value is a
meaningful default. Per-component `Option`s are functional because the common
component passes none.

**`Logger` is `*slog.Logger`, from the standard library — not `svcrt/logging`.**
`log/slog` is stdlib, so accepting one adds no `require` directive and does not
couple `lifecycle` to this project's logging module; a caller passes whatever
`*slog.Logger` they have. Nil means silent, which keeps the zero `Config`
usable. P6 is satisfied because the logger is *supplied*, never reached for
globally — `lifecycle` never touches `slog.Default()`.

It earns its place beyond the §3.5 warning: boot order is exactly what you want
logged when a service will not start, and component start/stop transitions are
otherwise invisible. The PoC logged them for the same reason.

### 3.2 The component model

**A component's `Start` returns when the component is ready.** This is what
makes a dependency edge meaningful: an edge waits on *ready*, and a `Start` that
blocked for the process lifetime could never be depended upon — the level above
it would never run.

Long-running work is launched into a goroutine the component owns:

```go
lc.Add("server",
    func(ctx context.Context) error {
        ln, err := net.Listen("tcp", addr)
        if err != nil {
            return err                       // not ready; nothing to clean up
        }
        go func() {
            if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
                lc.Fatal(err)
            }
        }()
        return nil                            // ready: listening
    },
    srv.Shutdown,
    lifecycle.After(db))
```

**The contract `Start` must honour**, stated here because §3.4 depends on it:

- Return only when ready, or return an error.
- Honour `ctx` cancellation while starting.
- On returning an error, leave nothing behind. `Stop` is never called for a
  component whose `Start` did not return `nil`.

### 3.3 `Fatal` is a method, not a channel

A post-start failure — a listener dying at 3am, a consumer losing its broker —
must trigger the same ordered shutdown a signal would.

```go
func (l *Lifecycle) Fatal(err error)
```

A `chan<- error` was rejected. It forces buffer-capacity arithmetic, and if the
buffer fills or `Run` has already returned, the sender blocks forever and leaks
the goroutine it was reporting from — an error-reporting path that hangs
precisely when there is an error. The method records the first error under a
`sync.Once` and signals once: non-blocking, safe from any goroutine, safe after
`Run` returns, and safe to call more than once.

### 3.4 `Run`

1. **Plan.** Compute levels from the declared edges.
2. **Start**, level by level, concurrently within a level.
3. **Wait** until `ctx.Done()` or a `Fatal` call.
4. **Drain.** Run `OnDrain` hooks, wait `DrainDelay`, then stop.
5. **Stop** in reverse level order, concurrently within a level.
6. Return the first fatal error, or `nil`.

**Startup failure (step 2).** When a `Start` returns an error while siblings are
still starting:

1. Cancel the level's context.
2. **Wait** for every in-flight `Start` to return. Never abandon one.
3. Stop every component whose `Start` returned `nil`, in reverse level order.
4. Return `errors.Join` of all failures, not merely the first.

Waiting rather than returning immediately is what keeps the unwind correct: a
`Start` abandoned mid-flight may have bound a listener or opened a pool that
nothing is now tracking, and `Stop` can never be called for a `Start` that never
returned. Cancelling rather than waiting for natural completion is what keeps a
doomed boot from hanging for the duration of its slowest component.

### 3.5 Draining

`OnDrain` hooks run **before** any component is stopped. Their purpose is to
flip readiness:

```go
lc.OnDrain(h.Drain)
```

This one line is the whole health/lifecycle seam. Neither module imports the
other; the coupling is visible in the user's own `main`, which is this project's
stated posture — the user writes `main`, always.

**It is a method value, not a closure.** `httpserver.Health.Drain` (§4.2) is
*the* drain hook, so there is no chance of writing `h.Live.Set(false)` by
mistake, and the line stays short enough to read as one idea.

**Full automation was considered and is not available.** For `lifecycle` to flip
readiness itself it must know what readiness is — an import. For `httpserver` to
register itself it must know what a lifecycle is — the other import. Either
direction reintroduces exactly what §1.1 removed, and makes `lifecycle`
unusable for a process with no HTTP.

Doing it in `Server.Shutdown` is also wrong, though it looks tempting now that
`Server` and `Health` share a module: `Shutdown` runs in the stop phase, which
is **after** `DrainDelay`. Readiness must go false, *then* the delay lets the
load balancer notice, *then* the server stops accepting. Flipping it in
`Shutdown` puts the delay on the wrong side and drops precisely the traffic it
exists to protect. The same ordering flaw rules out registering health as a
component added last so that it stops first.

**Omission is warned about, not enforced.** A forgettable line whose absence
silently disables graceful drain is a smell, so if `Run` has components
registered and **no** `OnDrain` hook, it logs a warning as draining begins. Not
an error: a CLI, a one-shot job, or a worker with no readiness concept
legitimately has none.

**`DrainDelay` is an addition to parent §9 and is justified here.** Flipping
readiness to false accomplishes nothing if the server stops accepting in the
same millisecond: the load balancer has not re-read the probe yet, so it keeps
routing to a socket that is closing, and those requests are dropped. The
interval between "readiness goes false" and "stop accepting" is the entire
reason readiness flips at all. It defaults to `0` — opt-in, no surprise — and
without it, graceful shutdown is graceful only for requests already in flight.

### 3.6 Two landmines, designed around

**Stop must not inherit the cancelled context.** By the time draining begins,
the run context is already cancelled. A `Stop` context derived from it is
cancelled at birth, so every `Shutdown` returns instantly and drains nothing —
which looks like a fast, clean shutdown and is actually data loss.

Stop contexts are built with **`context.WithoutCancel(runCtx)`** plus the
component's `StopTimeout`. Cancellation is shed; values — a request-scoped
logger, trace context — survive. The PoC used bare `context.Background()` here,
which drops those values silently.

**A second signal must still kill the process.** `SignalContext` cancels on the
first SIGINT/SIGTERM and then **restores default signal behaviour**, so an
operator who presses Ctrl-C twice, or a Kubernetes SIGKILL chaser, gets what
they expect. `lifecycle` still never calls `os.Exit`; the operating system does.
A drain that ignores the second signal is the classic way a pod hangs until its
termination grace period expires.

### 3.7 Cycles are unrepresentable

Edges take `Ref` values returned by `Add`, not names:

```go
db     := lc.Add("db",     openDB,    closeDB)
cache  := lc.Add("cache",  openCache, closeCache, lifecycle.After(db))
_       = lc.Add("server", srv.Start, srv.Shutdown, lifecycle.After(db, cache))
```

An edge can only reference an already-declared component, so a cycle cannot be
expressed. **This deletes cycle detection, its error kind, and its tests.** It
also makes a typo'd or missing dependency a compile error rather than a boot
failure in the one environment where someone mistyped it.

String names (`DependsOn("db")`) were rejected. They buy forward references —
declaring `server` before `db` — and that flexibility is exactly what forces
cycle detection, a missing-name error path, and a class of failure that only
appears at runtime. R0 twice ruled that compile-time beats runtime for this
trade: `contract.Detailed` is a named interface rather than a structural
assertion for the same reason.

Declaration order becomes dependency order, which is what a reader would write
anyway. It does **not** constrain execution: the graph is still a DAG, levels
are still computed from edges, and independent components still start
concurrently.

---

## 4. `httpserver`

### 4.1 Server surface

```go
package httpserver

type Options struct {
    Addr              string
    ReadHeaderTimeout time.Duration   // never zero; see §4.4
    ReadTimeout       time.Duration
    WriteTimeout      time.Duration
    IdleTimeout       time.Duration
    MaxHeaderBytes    int
    OnServeError      func(error)
}

func New(h http.Handler, opts Options) *Server
func (s *Server) Start(ctx context.Context) error   // binds; returns once LISTENING
func (s *Server) Shutdown(ctx context.Context) error
func (s *Server) Addr() string                      // resolved: ":0" yields the real port

type Middleware func(http.Handler) http.Handler
func Chain(ms ...Middleware) Middleware
```

`Options` is a struct per `docs/conventions.md` §1 — it mirrors `http.Server`'s
knobs field for field, so a reader who knows one knows the other.

**`Start` returning once listening is exactly `lifecycle.StartFunc`.** That is
deliberate, and it is how the two modules compose with no import between them:

```go
srv := httpserver.New(handler, httpserver.Options{Addr: cfg.Addr, OnServeError: lc.Fatal})
lc.Add("server", srv.Start, srv.Shutdown, lifecycle.After(store))
```

`OnServeError` is the same seam shape as the drain wiring: `main` connects the
two in one visible line, and `httpserver` never learns what a lifecycle is.

`Addr()` exists for tests — bind `:0`, ask what you got — so no suite ever races
another on a fixed port.

### 4.2 Health

```go
type Gate struct{ /* atomic.Bool; implements http.Handler */ }
func (g *Gate) Set(ok bool)

type Health struct{ Started, Live, Ready *Gate }
func NewHealth() *Health
func (h *Health) AddReadyCheck(name string, fn func(context.Context) error)
func (h *Health) Drain()    // sets Ready false; the drain hook (§3.5)
```

Three probes, because Kubernetes has three and conflating them causes real
outages:

- **startup** — has boot completed
- **liveness** — is the process wedged; **dependency-independent, deliberately**
- **readiness** — can we serve now; dependency-gated, false during drain

**Checks can attach only to readiness, and that is enforced by the API rather
than by documentation.** There is deliberately no `AddLiveCheck`. A liveness
probe that fails when the database is unreachable makes Kubernetes restart a
perfectly healthy process during a database outage, converting a degraded
service into a crash-loop across every replica at once. This is the single most
common way these three probes are misused, so the mistake is made
unrepresentable rather than discouraged.

**Response bodies carry codes, never prose**, per R0 §2. A failing readiness
returns 503 with `{"failed":["db"]}` — check *names*, which are identifiers.
The check's error text goes to your log, exactly as R0's `writeError` refuses
to serialize a non-`Coded` cause.

A hung check cannot hang the probe: each runs under a bounded timeout, and a
timeout counts as a failure.

### 4.3 The admin server

Every instance wants an admin surface, and it belongs on a **separate port**
from application traffic — not publicly routable, not behind the application's
auth middleware, not sharing timeouts tuned for real requests. A background
worker with no application HTTP surface has only this one.

```go
func AdminMux(h *Health) *http.ServeMux   // three probes, paths below
```

```go
package httpserver/pprof                  // SEPARATE package — see below
func Handler() http.Handler
```

`AdminMux` mounts exactly three routes, and they are named here rather than left
to "conventional" because a probe path is part of a deployment's contract with
Kubernetes:

| Route | Gate |
|---|---|
| `GET /healthz`  | `Live` |
| `GET /readyz`   | `Ready` |
| `GET /startupz` | `Started` |

`pprof.Handler()` returns a single `http.Handler` routing the whole
`/debug/pprof/` subtree internally, so it is mounted once at that prefix — Go's
`net/http/pprof` exposes five separate handlers, and requiring a caller to mount
each is how one gets forgotten.

`AdminMux` returns a plain `*http.ServeMux`, so adding an endpoint needs no API
from us:

```go
admin := httpserver.AdminMux(h)
admin.Handle("/debug/pprof/", pprof.Handler())   // svcrt/httpserver/pprof
// R3 mounts telemetry's metrics handler here
```

Returning a mux you extend keeps parent §9's "nothing served at a fixed path by
default" true — you opt in by calling `AdminMux`, and the gates remain mountable
individually anywhere.

**pprof lives in its own package, and the reason is a stdlib side effect that
cannot be avoided.**

`net/http/pprof` has an `init()` that registers its handlers on
`http.DefaultServeMux`. **This fires on any import, not only a blank one** —
verified empirically: a non-blank `import "net/http/pprof"` leaves
`DefaultServeMux` answering `GET /debug/pprof/` with 200. An earlier draft of
this spec claimed a `PprofHandler` in `httpserver` would "touch no global";
that claim was false.

The consequence matters more for a library than an application. If `httpserver`
imported `net/http/pprof`, then **every consumer of `httpserver` would silently
acquire pprof endpoints on their `DefaultServeMux`** — including one whose
legacy code actually serves it. That is an information disclosure introduced by
a dependency the user never asked for.

So `Handler()` lives in **`svcrt/httpserver/pprof`**, a separate package in the
same module. Importing `httpserver` pulls nothing; a user who wants profiling
imports the subpackage explicitly and thereby opts into the same side effect
they would get importing `net/http/pprof` themselves.

Its doc comment states the side effect plainly: importing this package
registers pprof handlers on `http.DefaultServeMux`, which is inert unless
something serves that mux — and `svclint`'s `defaults` pass already bans
serving it.

`Handler()` returns a mux routing the whole `/debug/pprof/` subtree, so it is
mounted once at that prefix rather than five times. It is never mounted by
default: profiling endpoints are a genuine information disclosure if that port
is ever routable.

### 4.4 `ReadHeaderTimeout` is never zero

If `Options.ReadHeaderTimeout` is left unset it receives a default rather than
Go's "wait forever". An unset `ReadHeaderTimeout` is a Slowloris hole: a handful
of connections dribbling headers one byte at a time pin server goroutines
indefinitely. This is the specific instance of parent §9's "timeout defaults
that are hard to get wrong."

---

## 5. The exemplar

`examples/orders` gains both modules, and `main.go` loses its hand-rolled
`http.Server` block along with R0's `// R1 replaces this` marker.

```go
lc := lifecycle.New(lifecycle.Config{DrainDelay: cfg.DrainDelay})
h  := httpserver.NewHealth()
lc.OnDrain(h.Drain)

store := lc.Add("store", st.Open, st.Close)

api := httpserver.New(newServer(svc, log), httpserver.Options{
    Addr: cfg.Addr, OnServeError: lc.Fatal,
})
lc.Add("api", api.Start, api.Shutdown, lifecycle.After(store))

admin := httpserver.New(httpserver.AdminMux(h), httpserver.Options{
    Addr: cfg.AdminAddr, OnServeError: lc.Fatal,
})
lc.Add("admin", admin.Start, admin.Shutdown, lifecycle.After(store))

ctx, stop := lifecycle.SignalContext(context.Background())
defer stop()

h.Started.Set(true)
h.Ready.Set(true)
if err := lc.Run(ctx); err != nil {
    log.Error("stopped", "err", err)
    os.Exit(1)
}
```

`api` and `admin` are independent — both depend only on `store` — so they occupy
the same level and start concurrently. That is the parallel path exercised by
the exemplar rather than only by unit tests.

A second example, **`examples/worker`, with its own `go.mod` and a `use` entry in
`go.work`** (matching how `examples/orders` is wired), carries no application
HTTP surface at all. It exists to prove the module boundaries hold for a
non-service process: a consumer component, an admin server depending on it, and
nothing else. Its `AdminMux` is its only listener.

Like `examples/orders` it stays out of `scripts/ci.sh`'s `MODULES` list — that
list is for library modules run with `GOWORK=off` to prove each stands alone —
and runs instead in the workspace-active block. It is mutation-gated the same
way `examples/orders` is, through a synthesised temporary module context, with
`main.go` excluded.

---

## 6. Acceptance criteria

R1 is done when these hold in `examples/orders`:

1. Components start in dependency order; `api` never starts before `store`.
2. `api` and `admin` start concurrently — both are at the same level.
3. After boot, startup and readiness both return 200.
4. On SIGTERM, **readiness returns 503 before the server stops accepting**.
5. A request in flight when the signal arrives **completes successfully**.
6. Components stop in exact reverse order.
7. **Liveness returns 200 throughout the drain.** The process is not wedged, and
   a liveness failure during shutdown invites a restart mid-drain.
8. A post-start `Serve` error reaches `Fatal` and triggers the same ordered
   drain as a signal.
9. A failed `Start` at a level stops only the components that started, in
   reverse order, and returns a joined error.

Criteria 4 and 5 together are the point of R1; the rest is scaffolding around
them.

---

## 7. Testing and CI

R0's gates carry over unchanged: **100% statement coverage** per library module,
**mutation score ≥ 0.85**, and every surviving mutant either killed or justified
in `docs/mutation-survivors.md` with both an equivalence proof and the reason
the guarded code exists.

**One addition, because R0's gates are blind to concurrency.** Coverage and
mutation testing say nothing about a race or a deadlock, and R0 shipped a
25%-flaky test that survived eleven review passes. `lifecycle` is the most
concurrent code in the project.

- `lifecycle` and `httpserver` tests run **`-race -count=10`** in CI. A lifecycle
  bug is far likelier to be "hangs one run in fifty" than "returns the wrong
  value".
- **Every concurrency test carries a hard timeout** and fails rather than
  hanging, so a deadlock is a red build in seconds rather than a CI job killed
  at ten minutes with no diagnostic.
- `testing.AllocsPerRun` is not used under `-race`; if an allocation assertion
  is needed it is guarded by the build-tagged skip R0 established.

Specific paths that get explicit tests because reasoning about them is not
enough:

- A level where one sibling fails and another is slow: assert the slow one is
  waited for, that only started components are stopped, and that the error joins
  both.
- A `Stop` that would block past its `StopTimeout`: assert the deadline fires
  and the remaining components still stop.
- `Stop` receiving a context with values but no cancellation, proving
  `WithoutCancel` is in place.
- A drain with an in-flight request, asserting the request completes.
- `Run` with components but no `OnDrain` hook, asserting the warning is emitted
  once and that a lifecycle with no components emits none.
- Ordering: readiness is already false when `DrainDelay` begins, not after it.

`examples/orders` gets the end-to-end drain test that is R1's acceptance gate:
bind `:0`, issue a slow request, signal, and assert readiness flipped to 503,
the slow request completed, and components stopped in reverse order.

`docs/conventions.md` gains two rules: the middleware-naming convention (three
spellings now exist — `contract.Middleware` a generic type, `logging.Middleware`
a constructor, `httpserver.Middleware` a plain type) and the `-count` policy for
concurrent modules.

---

## 8. Deviations from the parent spec

Recorded so they are decisions rather than drift.

| # | Parent | Deviation | Rationale |
|---|---|---|---|
| 1 | §9 lists `health` as a module | Folded into `httpserver` | §1.1 — a probe is an `http.Handler`; the two always ship together |
| 2 | §9 `lifecycle` | Adds `DrainDelay` | §3.5 — without it, readiness flipping false drops traffic anyway |
| 3 | §9 `lifecycle` "dependency edges" | Edges are typed `Ref`s, not names | §3.7 — makes cycles unrepresentable and typos compile errors |
| 4 | §12 R1 | Adds `examples/worker` | §5 — proves the boundaries hold for a non-service process |
| 5 | R0 testing gates | Adds `-race -count=10` and test timeouts | §7 — R0's gates are blind to concurrency |
| 6 | — | `Run` warns when components exist but no `OnDrain` hook does | §3.5 — omitting the drain hook silently disables graceful drain |
| 7 | — | pprof ships as `httpserver/pprof`, a separate package | §4.3 — importing `net/http/pprof` pollutes `DefaultServeMux` for every consumer |

---

## 9. Open questions

None blocking implementation.

Carried forward and **not** resolved here:

- **Parallel `Stop` within a level.** This spec stops concurrently within a
  level, symmetric with start. If a component turns out to require strictly
  sequential shutdown, the symmetry is what would break first. No current
  consumer needs it.
- **`telemetry`'s metrics handler** mounts onto `AdminMux` at R3. This spec
  fixes the mount point but ships no metrics.
