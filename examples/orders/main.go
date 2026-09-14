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
	"github.com/pavelpascari/svcrt/lifecycle"
	"github.com/pavelpascari/svcrt/logging"
	"github.com/pavelpascari/svcrt/telemetry"
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

	log := logging.New(os.Stdout, logging.Options{Level: slog.LevelInfo}, telemetry.LogExtractor())
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
