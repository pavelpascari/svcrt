# svcrt

Small, independent Go libraries for building services. Each module compiles
and is useful on its own, and none of them owns your process — you write
`main()`.

**Seven of the ten are stdlib-only.** `contract`, `config`, `logging`,
`lifecycle`, `httpserver`, `httpclient` and `resilience` have zero `require`
directives, asserted in CI rather than promised, so adopting one costs you
nothing. Three are documented exceptions: `telemetry` depends on the
OpenTelemetry API (never the SDK), and `kit` and `testkit` import siblings
because composing and testing svcrt is their entire job. Both kinds are
opt-in, and `docs/conventions.md` §11 argues each.

`svcrt` is the runtime half of a two-repo design. The generator half,
`svcgen`, is build-time only and never appears in a service's `go.mod`.

## Modules

| Module | What it does |
|---|---|
| `contract` | The type vocabulary generated code targets: error interfaces and a typed middleware seam. Imports only `context`. |
| `config` | Decodes environment variables into a struct once, at construction, reporting every violation at once. |
| `logging` | A `slog.Handler` that enriches records from their context. Defines no attribute keys of its own — each key belongs to whichever module emits it. |
| `lifecycle` | Starts components in dependency order, stops them in reverse, and drains before it stops. Owns no signals — you pass it a context. |
| `httpserver` | An `http.Server` with timeout defaults that are hard to get wrong, the three Kubernetes health gates, an admin mux, an `AccessLog` middleware that logs one line per request, and a `Recover` middleware that turns a panicking handler into a bare 500 and a logged stack — composed *innermost*, so the access line and the span both see the 500. |
| `httpclient` | An `http.Client` with connection-pool and timeout defaults that are hard to get wrong, and a `RoundTripper` middleware seam mirroring `httpserver`'s. |
| `resilience` | Retry with backoff, per-attempt timeouts, and a circuit breaker, as `RoundTripper` middleware. Refuses to retry what it cannot safely replay, and stops calling a dependency that is plainly dead. |
| `telemetry` | OpenTelemetry tracing and metrics for HTTP servers and clients, plus the log extractor that correlates them. The only module with external dependencies, and the API only — never the SDK. |
| `kit` | Composes the others into the stacks a service actually wants. Opt-in, and one of the two modules that import siblings. |
| `testkit` | Test helpers for services built on svcrt: log capture, a scripted stub upstream, and assertions on `contract` errors. Imports `contract` and `logging`, never `testing`. For consumers — svcrt's own core modules may not use it (`docs/conventions.md` §11). |

Each is versioned independently, with a per-module tag prefix —
`contract/v0.1.0`, `config/v0.1.0`. `contract` is *intended* to freeze at v1
once generated code exists to prove its shape, but that decision is still open
(design spec §10, D3) and it is not tagged v1 yet.

## Quick start

```go
type AppConfig struct {
    Addr  string        `env:"ADDR" default:":8080"`
    DBURL config.Secret `env:"DATABASE_URL"`
}

func main() {
    cfg, err := config.Load[AppConfig]()
    if err != nil {
        os.Stderr.WriteString(err.Error() + "\n") // every problem, at once
        os.Exit(1)
    }

    log := logging.New(os.Stdout, logging.Options{})
    log.Info("starting", "addr", cfg.Addr, "db", cfg.DBURL) // db redacts
}
```

See `examples/orders` for a complete service, and `examples/worker` for a
background process with no application HTTP surface.

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

`kit` exists so you do not have to remember any of this — it encodes all four
and is tested against each inversion. Reach for the raw modules when you
outgrow its defaults, and consult this table when you do.

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
directive gates syntax, not stdlib APIs, so only an old toolchain can hold
that compatibility promise honest. `mutation.sh` deliberately does **not** run
in CI; `docs/conventions.md` §12 records why, and what that costs.

`docs/conventions.md` records the decisions R0 made by doing rather than by
writing down — option shapes, what `Middleware` means, when to delete a clause
versus keep and test it, and why a supplied test suite is a floor.

`go.work` is a local convenience. CI runs the library modules with
`GOWORK=off`, because the workspace masks the version skew consumers would hit.
The exception is any module requiring a sibling at `v0.0.0` — `kit`, `testkit`
and the exemplars — which cannot build standalone until there is something to
depend on; CI detects that from disk and runs those with the workspace instead.

## Status

R9. All ten modules are implemented: `contract`, `config`, `logging`,
`lifecycle`, `httpserver`, `httpclient`, `resilience`, `telemetry`, `kit` and
`testkit`. `testkit` was the last one still listed as planned; R8 shipped it.
R9 added panic recovery, runnable examples, and the CI workflows that run the
gates this repo already had — until then `scripts/ci.sh` had only ever run in
a developer's shell.

**Nothing is tagged yet.** `git tag` is empty, which is why `kit`, `testkit`
and the exemplars require their siblings at `v0.0.0` and resolve through
`go.work`.
Until a first release, removing an exported name is free; after it, it is a
major version bump on every module that carries the name.

`health` was on that planned list and is not a module. R1 put the three
probes inside `httpserver` instead: they are served over HTTP, a worker with
no application HTTP surface still needs an admin listener to answer them
(`examples/worker`), and a module whose only job is to hand three booleans to
another module is a boundary that buys nothing. The entry is retired rather
than left dangling.
