# Runtime guide

`svcrt` is a set of small Go modules, not a framework. Pick the modules your
service needs, compose them with standard-library types, and keep ownership of
`main`, process exit, signals, telemetry SDK setup and deployment policy.

This guide starts with the shortest useful service and then adds the pieces
that production services commonly need. For API rationale and defaults, use
`go doc` or the package documentation; the recipes here focus on how the pieces
fit together.

## Install only what you use

Each top-level directory is an independently versioned Go module. The
repository has not published its first module tags yet, so use `@main` to try
the current revision:

```sh
go get github.com/pavelpascari/svcrt/config@main \
       github.com/pavelpascari/svcrt/httpserver@main \
       github.com/pavelpascari/svcrt/lifecycle@main \
       github.com/pavelpascari/svcrt/logging@main \
       github.com/pavelpascari/svcrt/telemetry@main
```

Go resolves `@main` to an immutable pseudo-version in `go.mod`. Commit
`go.mod` and `go.sum` so builds do not follow the branch. After releases begin,
prefer the module's release tag.

`kit` and `testkit` are the two exceptions during this pre-release period.
They require sibling modules at `v0.0.0`, which does not exist in the public
repository yet, so they currently work only inside this repository's
workspace. The recipes below show both the raw composition you can install
today and the shorter helpers that become consumable after the first sibling
tags are published. Do not add temporary `replace` directives to a production
service to work around the missing releases.

Seven modules have no dependencies outside the standard library. `telemetry`
depends only on the OpenTelemetry API, while `kit` and `testkit` compose sibling
svcrt modules. Importing one module does not pull in the rest.

## Build a small HTTP service

The minimum production-shaped stack has an application server, a separate
admin server for probes, and a lifecycle that opens readiness only after both
listeners start.

```go
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/lifecycle"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	health := httpserver.NewHealth()

	appMux := http.NewServeMux()
	appMux.HandleFunc("GET /hello/{name}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello, " + r.PathValue("name") + "\n"))
	})

	app := httpserver.New(
		httpserver.Chain(
			httpserver.AccessLog(log),
			httpserver.Recover(log),
		)(appMux),
		httpserver.Options{Addr: ":8080"},
	)
	admin := httpserver.New(
		httpserver.AdminMux(health),
		httpserver.Options{Addr: ":9090"},
	)

	lc := lifecycle.New(lifecycle.Config{Logger: log})
	lc.OnStarted(health.Up)
	lc.OnDrain(health.Drain)
	lc.Add("app", app.Start, app.Shutdown)
	lc.Add("admin", admin.Start, admin.Shutdown)

	ctx, stop := lifecycle.SignalContext(context.Background())
	defer stop()
	if err := lc.Run(ctx); err != nil {
		log.Error("service stopped", "err", err)
		os.Exit(1)
	}
}
```

Try it:

```sh
go run .
curl -i localhost:8080/hello/Ada
curl -i localhost:9090/healthz
curl -i localhost:9090/readyz
curl -i localhost:9090/startupz
```

Keep the admin listener private. Health probes and profiling endpoints should
not pass through public routing or application authentication.

## Load and test configuration

Every field needs an `env` tag or belongs to a nested `envPrefix` block. A
non-pointer field without a default is required. `Load` returns all violations
together, so fail startup once and show the operator the complete problem.

```go
type Config struct {
	Addr      string        `env:"ADDR" default:":8080"`
	AdminAddr string        `env:"ADMIN_ADDR" default:":9090"`
	DBURL     config.Secret `env:"DATABASE_URL"`
	Timeout   time.Duration `env:"UPSTREAM_TIMEOUT" default:"2s"`
}

cfg, err := config.Load[Config]()
if err != nil {
	return fmt.Errorf("load config: %w", err)
}
```

`config.Secret` redacts under `fmt`, JSON and `slog`. Convert it explicitly only
at the call site that needs the real value:

```go
db, err := sql.Open("postgres", string(cfg.DBURL))
```

Tests should inject a `Source` instead of changing process-wide environment
variables:

```go
func TestConfig(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"DATABASE_URL": "postgres://test",
		"UPSTREAM_TIMEOUT": "250ms",
	}
	source := config.Source(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})

	cfg, err := config.Load[Config](config.WithSource(source))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != 250*time.Millisecond {
		t.Fatalf("Timeout = %s", cfg.Timeout)
	}
}
```

For local development, layer the real environment ahead of a dotenv file so
deployment values always win:

```go
dot, err := config.Dotenv(".env")
if err != nil {
	return err
}
cfg, err := config.Load[Config](
	config.WithSource(config.Layer(config.OSEnv(), dot)),
)
```

## Order component dependencies

`Start` means "ready", not "run until shutdown". A component starts its own
goroutines and returns once dependents may safely use it. `After` then gives
the lifecycle both orders: dependencies start first and stop last.

```go
lc := lifecycle.New(lifecycle.Config{
	StopTimeout: 10 * time.Second,
	Logger:      log,
})

database := lc.Add("database", store.Open, store.Close)
consumer := lc.Add(
	"consumer",
	worker.Start,
	worker.Stop,
	lifecycle.After(database),
)
lc.Add("admin", admin.Start, admin.Shutdown, lifecycle.After(consumer))
```

If a component fails after `Start` returned, report it with `Fatal`. For HTTP
servers, wire `OnServeError` before constructing the lifecycle component:

```go
lc := lifecycle.New(lifecycle.Config{Logger: log})
api := httpserver.New(handler, httpserver.Options{
	Addr:         cfg.Addr,
	OnServeError: lc.Fatal,
})
lc.Add("api", api.Start, api.Shutdown)
```

Without this edge, a listener can die while the process and its lifecycle keep
running.

## Build one outbound client per upstream

Compose the independently installable modules in this order: breaker outside
retry, and telemetry inside retry. That makes the breaker count logical calls
and gives every attempt its own span.

```sh
go get github.com/pavelpascari/svcrt/httpclient@main \
       github.com/pavelpascari/svcrt/resilience@main
```

```go
pricingClient := httpclient.New(httpclient.Options{
	Middleware: httpclient.Chain(
		resilience.Breaker(resilience.BreakerPolicy{}),
		resilience.Retry(resilience.Policy{}),
		telemetry.Client(telemetry.Options{}),
	),
})
```

After the first module releases, `kit.NewClient(kit.ClientOptions{})` is the
short form of this stack. Its zero value provides the same tuned transport,
retries, tracing and circuit breaker.

```go
req, err := http.NewRequestWithContext(ctx, http.MethodGet, pricingURL, nil)
if err != nil {
	return err
}
resp, err := pricingClient.Do(req)
if err != nil {
	return err
}
defer resp.Body.Close()
```

Create one client per upstream. A breaker protects one middleware instance and
is not keyed by host; sharing the client would let one dead dependency open the
circuit for healthy hosts.

Set a deadline on the request context for the logical operation. Add
`resilience.Timeout` only when each individual attempt also needs a smaller
bound. The order expresses which timeout you mean:

```go
// 500ms per attempt, up to the request context's overall deadline.
client := httpclient.New(httpclient.Options{
	Middleware: httpclient.Chain(
		resilience.Breaker(resilience.BreakerPolicy{}),
		resilience.Retry(resilience.Policy{}),
		resilience.Timeout(500*time.Millisecond),
	),
})
```

The default retry policy retries transport errors and status 429, 502, 503 and
504 for idempotent HTTP methods. It does not retry `POST` or `PATCH`. Opt a
method in only when the endpoint is safe to replay, normally because the
server enforces an idempotency key:

```go
policy := resilience.Policy{
	RetryMethods: []string{http.MethodGet, http.MethodPost},
}
client := httpclient.New(httpclient.Options{
	Middleware: httpclient.Chain(
		resilience.Breaker(resilience.BreakerPolicy{}),
		resilience.Retry(policy),
		telemetry.Client(telemetry.Options{}),
	),
})
```

Request bodies must also be replayable. `http.NewRequest` supplies `GetBody`
for `strings.Reader`, `bytes.Reader` and `bytes.Buffer`; an opaque `io.Reader`
is sent only once.

For mTLS or a custom CA, configure `httpclient.Options.TLSClientConfig`. Do not
replace the returned client's transport, because that discards its timeouts,
proxy support, HTTP/2 setup and middleware.

```go
client := httpclient.New(httpclient.Options{
	TLSClientConfig: tlsConfig,
	Middleware: httpclient.Chain(
		resilience.Breaker(resilience.BreakerPolicy{}),
		resilience.Retry(resilience.Policy{}),
		telemetry.Client(telemetry.Options{}),
	),
})
```

Release pooled idle connections during shutdown:

```go
lc.Add(
	"pricing-client",
	func(context.Context) error { return nil },
	func(context.Context) error {
		pricingClient.CloseIdleConnections()
		return nil
	},
)
```

## Correlate traces and logs

`telemetry` uses the OpenTelemetry API but does not choose or own an SDK. Wire
your exporter, resource, sampler and providers in the application. With the
global providers configured, the zero-value telemetry options use them.

Add `telemetry.LogExtractor` to include the current `trace_id` and `span_id` on
records created with `InfoContext`, `ErrorContext` and the other context-aware
methods:

```go
log := logging.New(
	os.Stdout,
	logging.Options{},
	telemetry.LogExtractor(),
)
log.InfoContext(r.Context(), "order loaded", "order_id", order.ID)
```

After the first module releases, `kit.NewLogger(os.Stdout,
kit.LoggerOptions{})` is the shorter equivalent.

On the server, tracing must wrap access logging, and panic recovery must be
innermost:

```go
handler := httpserver.Chain(
	telemetry.Server(telemetry.Options{}),
	httpserver.AccessLog(log),
	httpserver.Recover(log),
)(mux)
```

`kit.NewClient` encodes the same ordering once the composing module is
available to external consumers.

## Add readiness checks

Readiness may include cheap dependency checks. Name every check; clients see
the names of failed checks while detailed errors stay in logs.

```go
health := httpserver.NewHealth()
health.AddReadyCheck("database", store.Ping)
health.Ready.OnCheckError = func(name string, err error) {
	log.Error("readiness check failed", "check", name, "err", err)
}
```

Liveness is deliberately open from construction and independent of
dependencies. A dependency outage should remove the instance from traffic via
readiness, not cause a restart loop via liveness.

## Test behavior, not log strings

`testkit` supplies a scripted upstream, structured log capture, and assertions
for stable contract error codes. Until the first sibling module tags are
published, these helpers are available to the repository's own examples but
not to external modules; the APIs below are the intended post-release usage.

```go
func TestPricingRetriesThenRecovers(t *testing.T) {
	upstream := testkit.Upstream(t,
		testkit.Status(http.StatusServiceUnavailable),
		testkit.JSON(http.StatusOK, `{"amount": 42}`),
	)

	client := httpclient.New(httpclient.Options{
		Middleware: resilience.Retry(resilience.Policy{
			MaxAttempts: 2,
			Backoff:     resilience.Constant(time.Millisecond),
		}),
	})

	resp, err := client.Get(upstream.URL() + "/price")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := upstream.Requests(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}
```

Capture logs as records and assert on attributes rather than serialized JSON:

```go
log, records := testkit.Logger(t, telemetry.LogExtractor())
log.InfoContext(ctx, "order loaded", "order_id", "ord_1")

matches := records.Find("order_id", "ord_1")
if len(matches) != 1 {
	t.Fatalf("matching records = %d, want 1", len(matches))
}
```

For API errors, assert the stable wire vocabulary rather than prose:

```go
testkit.AssertCode(t, err, "order.not_found")
testkit.AssertParams(t, err, map[string]any{"id": "ord_1"})
```

## Runnable references

- [`examples/orders`](../examples/orders) — HTTP API, contract errors, store,
  upstream client, breaker, telemetry, two listeners and ordered shutdown.
- [`examples/worker`](../examples/worker) — background consumer with lifecycle
  and health probes, but no application HTTP server.
- [`examples/notifications`](../examples/notifications) — HTTP ingestion plus
  an asynchronous worker, with retry, circuit breaking, trace propagation
  across a queue and graceful draining of accepted work.
- Package `example_test.go` files — focused examples executed by `go test`, so
  their output and API usage cannot silently drift.

Run everything from the repository root:

```sh
./scripts/ci.sh
```

Or inspect one module's examples:

```sh
cd resilience
go test ./... -run Example -v
```
