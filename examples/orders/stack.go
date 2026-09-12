package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/lifecycle"
)

// appStack is the health/lifecycle/api/admin composition. It is built by
// buildStack, the one function main and the acceptance tests both call --
// so a regression in this wiring (a dropped OnDrain, an ignored DrainDelay,
// a missing After) fails in the test binary instead of only being visible in
// a running process.
type appStack struct {
	lc     *lifecycle.Lifecycle
	health *httpserver.Health
	api    *httpserver.Server
	admin  *httpserver.Server
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
	Trace      func(op string)
}

// buildStack wires health, lifecycle, and the two HTTP servers exactly as
// main runs them. Both main and the acceptance tests call this -- it is the
// seam that makes the drain behavior observable from a test rather than only
// from a running binary.
//
// The returned stack comes up ready: Started, Live, and Ready are all set
// true before this function returns, unconditionally, for both callers.
// That flag-setting used to live in main as three lines a future edit could
// silently drop -- unlike OnDrain or DrainDelay, a dropped Set(true) fails
// with no crash and no log line, only every readiness probe returning 503
// forever, which surfaces as a stalled rollout rather than an error anyone
// sees. Moving it here removes the line from main that could be forgotten.
// A service that genuinely needs to warm up before accepting traffic calls
// health.Ready.Set(false) right after buildStack returns -- one explicit
// line undoing the default, not three omitted ones creating a silent gap.
func buildStack(cfg appStackConfig) *appStack {
	trace := cfg.Trace
	if trace == nil {
		trace = func(string) {}
	}

	health := httpserver.NewHealth()
	lc := lifecycle.New(lifecycle.Config{DrainDelay: cfg.DrainDelay, Logger: cfg.Logger})

	// The whole health/lifecycle seam, in one visible line. Neither module
	// imports the other; the coupling lives here, in your code.
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

	health.Started.Set(true)
	health.Live.Set(true)
	health.Ready.Set(true)

	return &appStack{lc: lc, health: health, api: api, admin: admin}
}
