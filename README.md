# svcrt

Small, independent Go libraries for building services. Each module compiles
and is useful on its own, depends on nothing outside the standard library, and
never owns your process — you write `main()`.

`svcrt` is the runtime half of a two-repo design. The generator half,
`svcgen`, is build-time only and never appears in a service's `go.mod`.

## Modules

| Module | What it does |
|---|---|
| `contract` | The type vocabulary generated code targets: error interfaces and a typed middleware seam. Imports only `context`. |
| `config` | Decodes environment variables into a struct once, at construction, reporting every violation at once. |
| `logging` | A `slog.Handler` that enriches records from their context. Defines no attribute keys of its own — each key belongs to whichever module emits it. |
| `lifecycle` | Starts components in dependency order, stops them in reverse, and drains before it stops. Owns no signals — you pass it a context. |
| `httpserver` | An `http.Server` with timeout defaults that are hard to get wrong, the three Kubernetes health gates, an admin mux, and an `AccessLog` middleware that logs one line per request. |
| `httpclient` | An `http.Client` with connection-pool and timeout defaults that are hard to get wrong, and a `RoundTripper` middleware seam mirroring `httpserver`'s. |
| `resilience` | Retry with backoff and per-attempt timeouts, as `RoundTripper` middleware. Refuses to retry what it cannot safely replay. |

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

## Development

```sh
./scripts/ci.sh                        # test every module in isolation
./scripts/mutation.sh                  # mutation gate, every module
./scripts/release.sh contract v0.1.0   # tag one module
```

`docs/conventions.md` records the decisions R0 made by doing rather than by
writing down — option shapes, what `Middleware` means, when to delete a clause
versus keep and test it, and why a supplied test suite is a floor.

`go.work` is a local convenience. CI runs the library modules with
`GOWORK=off`, because the workspace masks the version skew consumers would hit.

## Status

R3. `contract`, `config`, `logging`, `lifecycle`, `httpserver`, `httpclient`,
and `resilience` are implemented. `telemetry` and `testkit` are planned —
see `docs/superpowers/specs/`.

`health` was on that planned list and is not a module. R1 put the three
probes inside `httpserver` instead: they are served over HTTP, a worker with
no application HTTP surface still needs an admin listener to answer them
(`examples/worker`), and a module whose only job is to hand three booleans to
another module is a boundary that buys nothing. The entry is retired rather
than left dangling.
