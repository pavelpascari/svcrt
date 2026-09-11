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
| `logging` | A `slog.Handler` that enriches records from their context, plus shared attribute-key conventions. |

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

See `examples/orders` for a complete service.

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

R0. `contract`, `config`, and `logging` are implemented. `health`,
`lifecycle`, `httpserver`, `telemetry`, `httpclient`, `resilience`, and
`testkit` are planned — see `docs/superpowers/specs/`.
