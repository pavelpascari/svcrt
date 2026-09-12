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
	"os"

	"github.com/pavelpascari/svcrt/config"
	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/lifecycle"
	"github.com/pavelpascari/svcrt/logging"
)

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

	health := httpserver.NewHealth()
	lc := lifecycle.New(lifecycle.Config{DrainDelay: cfg.DrainDelay, Logger: log})

	// The whole health/lifecycle seam, in one visible line. Neither module
	// imports the other; the coupling lives here, in your code.
	lc.OnDrain(health.Drain)

	st := NewStore(map[string]*Order{"1": {ID: "1", Qty: 3}})
	store := lc.Add("store", st.Open, st.Close)

	api := httpserver.New(newServer(NewService(st), log), httpserver.Options{
		Addr: cfg.Addr, OnServeError: lc.Fatal,
	})
	lc.Add("api", api.Start, api.Shutdown, lifecycle.After(store))

	admin := httpserver.New(httpserver.AdminMux(health), httpserver.Options{
		Addr: cfg.AdminAddr, OnServeError: lc.Fatal,
	})
	lc.Add("admin", admin.Start, admin.Shutdown, lifecycle.After(store))

	ctx, stop := lifecycle.SignalContext(context.Background())
	defer stop()

	health.Started.Set(true)
	health.Live.Set(true)
	health.Ready.Set(true)

	if err := lc.Run(ctx); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
