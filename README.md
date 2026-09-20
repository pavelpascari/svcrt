# svcrt

Small, independent Go libraries for building services. Each module compiles
and is useful on its own, and none of them owns your process — you write
`main()`. Nothing here calls `os.Exit`, installs a signal handler, or writes to
a global logger.

**Seven of the ten are stdlib-only.** `contract`, `config`, `logging`,
`lifecycle`, `httpserver`, `httpclient` and `resilience` have zero `require`
directives, asserted in CI rather than promised, so adopting one costs you
nothing. Three are documented exceptions: `telemetry` depends on the
OpenTelemetry API (never the SDK), and `kit` and `testkit` import siblings
because composing and testing svcrt is their entire job. All three are opt-in,
and `docs/conventions.md` §11 argues each one.

`svcrt` is the runtime half of a two-repo design. The generator half,
`svcgen`, is build-time only and never appears in a service's `go.mod`.

## Start here

- **Building your first service?** Follow the [quick start](#quick-start), then
  use the [runtime guide](docs/runtime-guide.md) for clients, retries,
  dependency ordering, health checks and tests.
- **Looking for a complete application?** [`examples/orders`](examples/orders)
  is an HTTP API with an upstream dependency;
  [`examples/worker`](examples/worker) is a background process with probes but
  no application listener; and
  [`examples/notifications`](examples/notifications) combines an API and an
  asynchronous worker with a queue, provider retries and trace propagation.
- **Looking up one API?** The [module table](#modules) points to each package's
  main entry point. Every package also ships executable examples visible in
  `go doc` and on pkg.go.dev.

The repository has not published its first per-module tags yet. To try the
current revision in another module, select only the packages you need:

```sh
go get github.com/pavelpascari/svcrt/config@main \
       github.com/pavelpascari/svcrt/httpserver@main \
       github.com/pavelpascari/svcrt/lifecycle@main \
       github.com/pavelpascari/svcrt/logging@main \
       github.com/pavelpascari/svcrt/telemetry@main
```

For a reproducible build, commit the resulting `go.mod` and `go.sum`; Go records
an immutable pseudo-version rather than a moving `main` reference. Once module
tags are published, use the release tag instead.

## Modules

Each module is its own Go module under `github.com/pavelpascari/svcrt/`, so you
import only the ones you use.

| Module | What problem it solves | Start here |
|---|---|---|
| `contract` | Your error types need a stable, wire-facing vocabulary that generated code and hand-written code both target. Codes and scalar params cross the wire; server-authored prose never does. Imports only `context`. | `contract.Coded`, `contract.Detailed` |
| `config` | Reading the environment one `os.Getenv` at a time scatters validation and fails one variable per restart. `config` decodes a whole struct once, at construction, and reports **every** violation in a single error. | `config.Load[T]`, `config.Secret` |
| `logging` | Log lines inside a request should carry request-scoped context without the logging package knowing what a trace is. Returns a plain `*slog.Logger`; defines no attribute keys of its own. | `logging.New`, `logging.Extractor` |
| `lifecycle` | Components must start in dependency order, stop in reverse, and stop accepting traffic *before* they stop serving it. Owns no signals — you pass it a context. | `lifecycle.New`, `Lifecycle.Add`, `Lifecycle.Run` |
| `httpserver` | An `http.Server` with no timeouts is a Slowloris hole, and health probes conflated into one endpoint cause restart loops. Supplies timeout defaults, three separate probes, an admin mux, and `AccessLog`/`Recover` middleware. | `httpserver.New`, `httpserver.NewHealth`, `httpserver.AdminMux` |
| `httpclient` | A fresh `http.Transport` silently loses HTTP/2 and proxy support, and defaults to 2 idle connections per host. Supplies a tuned transport plus a `RoundTripper` middleware seam mirroring `httpserver`'s. | `httpclient.New` |
| `resilience` | Retries that replay a non-idempotent request duplicate its effect, and retrying a dependency that is plainly dead just adds load. Retry with backoff, per-attempt timeouts and a circuit breaker, all as `RoundTripper` middleware. | `resilience.Retry`, `resilience.Breaker`, `resilience.Timeout` |
| `telemetry` | Spans, metrics and logs are only useful if you can pivot between them. Server and client middleware plus the log extractor that puts `trace_id` on every line. The only module with external dependencies, and the OTel **API** only — never the SDK. | `telemetry.Server`, `telemetry.Client`, `telemetry.LogExtractor` |
| `kit` | Four middleware orderings in this repo fail silently when inverted. `kit` encodes them so you do not have to remember any of them. Opt-in; one of the two modules that import siblings. | `kit.NewClient`, `kit.NewLogger` |
| `testkit` | Testing a service built on svcrt means capturing log records, standing up a scripted upstream, and asserting on error codes rather than messages. Imports `contract` and `logging`, never `testing`. For consumers — svcrt's own core modules may not use it. | `testkit.Logger`, `testkit.Upstream`, `testkit.AssertCode` |

Each is versioned independently, with a per-module tag prefix —
`contract/v0.1.0`, `config/v0.1.0`. `contract` is *intended* to freeze at v1
once generated code exists to prove its shape, but that decision is still open
and it is not tagged v1 yet.

## Quick start

A complete service: config from the environment, a trace-correlated logger, an
application listener, an admin listener answering the probes, and an ordered
drain on SIGTERM.

```go
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/pavelpascari/svcrt/config"
	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/lifecycle"
	"github.com/pavelpascari/svcrt/logging"
	"github.com/pavelpascari/svcrt/telemetry"
)

type AppConfig struct {
	Addr      string        `env:"ADDR" default:":8080"`
	AdminAddr string        `env:"ADMIN_ADDR" default:":9090"`
	DBURL     config.Secret `env:"DATABASE_URL"`
}

func main() {
	cfg, err := config.Load[AppConfig]()
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n") // every problem, at once
		os.Exit(1)
	}

	// The extractor makes every line logged with a request context carry its
	// trace_id. logging.New alone deliberately knows nothing about tracing.
	log := logging.New(
		os.Stdout,
		logging.Options{Level: slog.LevelInfo},
		telemetry.LogExtractor(),
	)
	log.Info("starting", "addr", cfg.Addr, "db", cfg.DBURL) // db redacts

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + r.PathValue("id") + `"}`))
	})

	// Order matters -- see Composition order below.
	handler := httpserver.Chain(
		telemetry.Server(telemetry.Options{}),
		httpserver.AccessLog(log),
		httpserver.Recover(log),
	)(mux)

	health := httpserver.NewHealth()
	api := httpserver.New(handler, httpserver.Options{Addr: cfg.Addr})
	admin := httpserver.New(httpserver.AdminMux(health), httpserver.Options{Addr: cfg.AdminAddr})

	lc := lifecycle.New(lifecycle.Config{Logger: log})

	// Neither module imports the other: the health/lifecycle coupling lives
	// here, in your code, as a pair of method values.
	lc.OnStarted(health.Up)
	lc.OnDrain(health.Drain)

	lc.Add("api", api.Start, api.Shutdown)
	lc.Add("admin", admin.Start, admin.Shutdown)

	ctx, stop := lifecycle.SignalContext(context.Background())
	defer stop()

	if err := lc.Run(ctx); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
```

`DATABASE_URL` has no default, so an unset one fails the boot rather than
booting half-configured. With it set, the process writes:

```
{"time":"...","level":"INFO","msg":"starting","addr":":8080","db":"[REDACTED]"}
{"time":"...","level":"INFO","msg":"http request","method":"GET","route":"GET /orders/{id}","status":200,"duration_ms":0}
```

The `route` attribute is the matched `ServeMux` pattern, not the request path,
so it does not grow a distinct value per order id.

See `examples/orders` for a service with an upstream client, a circuit breaker
and a dependency-ordered store, and `examples/worker` for a background process
with no application HTTP surface. `examples/notifications` is the advanced
composition: an API hands traced work to a queue, a lifecycle-managed worker
retries an idempotent provider call, and shutdown drains accepted work.

The [runtime guide](docs/runtime-guide.md) builds on this example with focused,
copyable recipes for adding dependencies, outbound clients, readiness checks,
configuration tests and service tests.

## Health probes and the admin surface

`httpserver.NewHealth()` returns the three gates Kubernetes distinguishes, and
`httpserver.AdminMux(h)` serves exactly three routes — no more:

| Route | Gate | Means |
|---|---|---|
| `GET /healthz` | `Live` | the process is not wedged. **Open from construction**, and deliberately dependency-independent |
| `GET /readyz` | `Ready` | this instance can serve now. Closed until startup finishes, closed again on drain, and gated by any `AddReadyCheck` you registered |
| `GET /startupz` | `Started` | boot has completed |

There is no `/livez`; a request for one gets a 404. `AdminMux` returns a plain
`*http.ServeMux`, so you can add your own routes to it — including
`svcrt/httpserver/pprof`, which is a separate package precisely so importing
`httpserver` never registers profiling endpoints you did not ask for.

Each gate answers `200` when open and `503` when closed. A readiness failure
responds with the **names** of the checks that failed and nothing else; the
errors themselves go to `Gate.OnCheckError`, which you wire to your logger.

Serve the admin mux on a port separate from application traffic, so it is not
publicly routable, not behind the application's auth middleware, and not
sharing timeouts tuned for real requests.

`Live` is open from the moment the process can answer a request, and that is
not an oversight. A failing liveness probe asks the orchestrator to **restart**
the process, so a `Live` that stayed closed until boot finished would turn a
slow start into a restart loop. `Started` is the gate that answers "has boot
finished".

## Composition order

Middleware order is load-bearing in four places, and **every one of them fails
silently when inverted** — no error, no warning, just observability that
quietly stops working. Each has a test that fails if the order is wrong; this
is the summary.

```go
// server: Recover innermost, so the 500 it writes is seen by everything outside
h := httpserver.Chain(
    telemetry.Server(telemetry.Options{}),  // span exists before the log line
    httpserver.AccessLog(log),
    httpserver.Recover(log),                // innermost
)(mux)

// client: Breaker outside Retry, telemetry inside it
c := kit.NewClient(kit.ClientOptions{})     // does exactly this, correctly

// logger: without the extractor, spans and metrics are right and logs are uncorrelated
log := kit.NewLogger(os.Stdout, kit.LoggerOptions{})
```

| get this wrong | what breaks |
|---|---|
| `telemetry.Server` inside `AccessLog` | the access line loses **both** its `trace_id` and its route — `Server` rebinds the request, and `ServeMux` records the pattern on the rebound one |
| `Recover` outside `AccessLog` | a panic unwinds inner defers first, so the line is written before the 500 exists and records no status at all |
| `telemetry.Client` outside `Retry` | three attempts collapse into one span; the retries become invisible |
| a logger built without `telemetry.LogExtractor` | correct spans, correct metrics, and logs you cannot pivot from |

One more, on the client side, that is a correctness rather than an
observability failure: `resilience.Breaker` belongs **outside** `Retry`, so it
counts logical calls rather than attempts. Inside, a single three-attempt burst
against a briefly flaky upstream trips a threshold-3 breaker even though the
call ultimately succeeded.

`kit` exists so you do not have to remember any of this — `kit.NewClient`
composes `Breaker → Retry → telemetry.Client → your middleware → transport`,
and `kit.NewLogger` wires the extractor. Both are tested against each
inversion. Reach for the raw modules when you outgrow their defaults, and
consult this table when you do.

## Documentation

```sh
go doc ./resilience                    # package synopsis
go doc ./resilience Breaker            # one symbol
./scripts/docs.sh                      # browse everything at localhost:8080
```

`go doc` needs nothing and is the answer for a single lookup. `scripts/docs.sh`
runs `pkgsite` against the workspace and renders every module the way
pkg.go.dev does; it prints the one-line install command if `pkgsite` is absent.

Every module ships `Example` functions, and they are documentation that cannot
rot: an example with an `// Output:` comment is **executed** by `go test` and
its output compared, so a signature change or a behaviour change breaks the
build rather than leaving a stale code block behind.

```sh
cd resilience && go test ./... -run Example -v
```

## Development

```sh
./scripts/ci.sh                        # test every module in isolation
./scripts/coverage.sh                  # coverage ratchet against coverage-floors.txt
./scripts/mutation.sh                  # mutation gate, every module
./scripts/release.sh contract v0.1.0   # tag one module
```

`ci.sh` and `coverage.sh` run on every push and pull request
(`.github/workflows/ci.yml`), and `contract` is additionally built and tested
on a real Go 1.22 toolchain (`.github/workflows/compat.yml`) — the `go`
directive gates syntax, not stdlib APIs, so only an old toolchain can hold that
compatibility promise honest. `mutation.sh` deliberately does **not** run in
CI; `docs/conventions.md` §12 records why, and what that costs.

`docs/conventions.md` records the decisions this repo made by doing rather than
by writing down — option shapes, what `Middleware` means, when to delete a
clause versus keep and test it, and why a supplied test suite is a floor.
`docs/mutation-survivors.md` records what the mutation runs found.

`go.work` is a local convenience. CI runs the library modules with
`GOWORK=off`, because the workspace masks the version skew consumers would hit.
The exception is any module requiring a sibling at `v0.0.0` — `kit`, `testkit`
and the exemplars — which cannot build standalone until there is something to
depend on; CI detects that from disk and runs those with the workspace instead.

## Status

All ten modules are implemented and tested: `contract`, `config`, `logging`,
`lifecycle`, `httpserver`, `httpclient`, `resilience`, `telemetry`, `kit` and
`testkit`, plus the `httpserver/pprof` subpackage and three runnable exemplars
under `examples/`.

**Nothing is tagged yet.** `git tag` is empty, which is why `kit`, `testkit`
and the exemplars require their siblings at `v0.0.0` and resolve through
`go.work`, and why `go get` against these paths will not resolve a version.
Until a first release, removing an exported name is free; after it, it is a
major version bump on every module that carries the name.

There is no `health` module. The three probes live inside `httpserver`: they
are served over HTTP, a worker with no application HTTP surface still needs an
admin listener to answer them (`examples/worker`), and a module whose only job
is to hand three booleans to another module is a boundary that buys nothing.
