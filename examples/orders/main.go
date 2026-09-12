// Command orders is the svcrt R1 exemplar: a complete service composed from
// contract, config, logging, lifecycle, and httpserver with nothing else.
//
// It is deliberately assembled by hand. Every runtime API it touches must be
// pleasant to call from generated code, not just from a human -- that is what
// this file exists to check before the generator is written.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/pavelpascari/svcrt/config"
	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/lifecycle"
	"github.com/pavelpascari/svcrt/logging"
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

	return &appStack{lc: lc, health: health, api: api, admin: admin}
}

func main() {
	// The user writes main. Always. Nothing in svcrt calls os.Exit, installs a
	// signal handler, or writes to a global logger -- so the exits below are
	// here, in your code, where you can see them.
	cfg, err := config.Load[AppConfig]()
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}

	log := logging.New(os.Stdout, logging.Options{Level: slog.LevelInfo})
	log.Info("starting",
		"addr", cfg.Addr,
		"admin_addr", cfg.AdminAddr,
		"otel_endpoint", cfg.OTel.Endpoint,
		"db", cfg.DBURL, // Secret: redacts to [REDACTED]
	)

	st := NewStore(map[string]*Order{"1": {ID: "1", Qty: 3}})

	s := buildStack(appStackConfig{
		APIAddr:    cfg.Addr,
		AdminAddr:  cfg.AdminAddr,
		DrainDelay: cfg.DrainDelay,
		Logger:     log,
		Handler:    newServer(NewService(st), log),
		StoreOpen:  st.Open,
		StoreClose: st.Close,
	})

	// Everything below this line is the genuinely untestable part: a real
	// OS signal handler, and the process-level Run loop that blocks until
	// one arrives. go test never calls main, so nothing here is observable
	// by the suite -- which is exactly why the wiring above was extracted
	// into buildStack instead of living here.
	ctx, stop := lifecycle.SignalContext(context.Background())
	defer stop()

	s.health.Started.Set(true)
	s.health.Live.Set(true)
	s.health.Ready.Set(true)

	if err := s.lc.Run(ctx); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
