package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/pavelpascari/svcrt/httpclient"
	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/lifecycle"
	"github.com/pavelpascari/svcrt/resilience"
	"github.com/pavelpascari/svcrt/telemetry"
)

// appStack is the health/lifecycle/api/admin composition. It is built by
// buildStack, the one function main and the acceptance tests both call --
// so a regression in this wiring (a dropped OnDrain, an ignored DrainDelay,
// a missing After) fails in the test binary instead of only being visible in
// a running process.
type appStack struct {
	lc      *lifecycle.Lifecycle
	health  *httpserver.Health
	api     *httpserver.Server
	admin   *httpserver.Server
	pricing *PricingClient
}

// appStackConfig carries the two things a test wiring must supply
// differently from production: the API's handler (the acceptance tests
// serve a /slow endpoint the real service does not have), and the store's
// start/stop functions (so main can pass the real Store's Open/Close while
// tests pass their own). Trace is optional instrumentation: production
// leaves it nil, and the acceptance tests use it to record start/stop order
// without re-implementing this wiring.
type appStackConfig struct {
	APIAddr    string
	AdminAddr  string
	DrainDelay time.Duration
	Logger     *slog.Logger
	Handler    http.Handler
	StoreOpen  lifecycle.StartFunc
	StoreClose lifecycle.StopFunc
	PricingURL string
	Trace      func(op string)
}

// buildStack wires health, lifecycle, and the two HTTP servers exactly as
// main runs them. Both main and the acceptance tests call this -- it is the
// seam that makes the drain behavior observable from a test rather than only
// from a running binary.
//
// The returned stack is NOT yet ready: Started and Ready open when Run has
// finished starting every component, via the OnStarted hook below. Live is
// already open, because NewHealth opens it -- a liveness gate that waits for
// boot restarts a slow-starting process.
//
// Those gates used to be three Set(true) calls at the end of this function,
// which was both easy to drop silently and wrong about time. Wrong about
// time because api and admin are the same level and start concurrently, so
// the gates were already open while api was still binding: /readyz could
// answer 200 for a service that could not yet serve. Easy to drop because
// unlike a missing OnDrain, a missing Set(true) fails with no crash and no
// log line -- just every readiness probe returning 503 forever, surfacing as
// a stalled rollout rather than an error anyone sees.
//
// Handing both edges to the lifecycle fixes both problems at once: the hooks
// are registered next to each other, and the only thing that knows when
// startup actually finished is the thing that did the starting. A service
// that must warm up before accepting traffic adds its own OnStarted hook
// after this one, or registers the warm-up as a component.
func buildStack(cfg appStackConfig) *appStack {
	trace := cfg.Trace
	if trace == nil {
		trace = func(string) {}
	}

	health := httpserver.NewHealth()
	lc := lifecycle.New(lifecycle.Config{DrainDelay: cfg.DrainDelay, Logger: cfg.Logger})

	// The whole health/lifecycle seam, in two visible lines. Neither module
	// imports the other; the coupling lives here, in your code, as a pair of
	// method values.
	lc.OnStarted(health.Up)
	lc.OnDrain(health.Drain)

	store := lc.Add("store",
		func(ctx context.Context) error { trace("start:store"); return cfg.StoreOpen(ctx) },
		func(ctx context.Context) error { trace("stop:store"); return cfg.StoreClose(ctx) })

	api := httpserver.New(cfg.Handler, httpserver.Options{
		Addr: cfg.APIAddr, OnServeError: lc.Fatal,
	})
	lc.Add("api",
		func(ctx context.Context) error { trace("start:api"); return api.Start(ctx) },
		func(ctx context.Context) error { trace("stop:api"); return api.Shutdown(ctx) },
		lifecycle.After(store))

	admin := httpserver.New(httpserver.AdminMux(health), httpserver.Options{
		Addr: cfg.AdminAddr, OnServeError: lc.Fatal,
	})
	lc.Add("admin",
		func(ctx context.Context) error { trace("start:admin"); return admin.Start(ctx) },
		func(ctx context.Context) error { trace("stop:admin"); return admin.Shutdown(ctx) },
		lifecycle.After(store))

	pricing := NewPricingClient(cfg.PricingURL, httpclient.New(httpclient.Options{
		Middleware: httpclient.Chain(
			// resilience returns the bare func(http.RoundTripper) http.RoundTripper,
			// so it is assignable to httpclient.Middleware with neither module
			// importing the other. A named type on both sides would not compile.
			resilience.Retry(resilience.Policy{
				MaxAttempts: 3,
				Backoff:     resilience.Constant(10 * time.Millisecond),
			}),
			// telemetry.Client goes INSIDE Retry, so one span is recorded per
			// attempt -- a retried call then shows the attempts that failed.
			// Outside, three attempts would collapse into one span.
			telemetry.Client(telemetry.Options{}),
		),
	}))
	lc.Add("pricing-client",
		func(context.Context) error { return nil },
		func(context.Context) error {
			trace("stop:pricing-client")
			pricing.CloseIdleConnections()
			return nil
		})

	return &appStack{lc: lc, health: health, api: api, admin: admin, pricing: pricing}
}
