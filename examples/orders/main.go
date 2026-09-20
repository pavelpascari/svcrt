// Command orders is a complete svcrt service: an HTTP API, an admin listener
// answering the Kubernetes probes, a circuit-broken client to an upstream, and
// an ordered startup and drain -- composed from config, contract, logging,
// lifecycle, httpserver, kit and telemetry, and nothing else.
//
// Read it as the worked example of the wiring the README summarises. main()
// here does only what a service's main() must do: load config, build the
// stack, install a signal handler, run. Everything else lives in stack.go and
// server.go, which the acceptance tests call directly.
//
// It is deliberately assembled by hand rather than generated. Every runtime
// API it touches has to be pleasant to call from generated code as well as by
// a person, and hand-writing the call sites is how that gets checked.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/pavelpascari/svcrt/config"
	"github.com/pavelpascari/svcrt/kit"
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

	log := kit.NewLogger(os.Stdout, kit.LoggerOptions{
		Logging: logging.Options{Level: slog.LevelInfo},
	})
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
		PricingURL: cfg.PricingURL,
	})

	// Everything below this line is the genuinely untestable part: a real
	// OS signal handler, and the process-level Run loop that blocks until
	// one arrives. go test never calls main, so nothing here is observable
	// by the suite -- which is exactly why buildStack, including the health
	// flags it sets, lives in stack.go instead of here. This file is the
	// only thing scripts/mutation.sh excludes, and it is now only main().
	ctx, stop := lifecycle.SignalContext(context.Background())
	defer stop()

	if err := s.lc.Run(ctx); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
