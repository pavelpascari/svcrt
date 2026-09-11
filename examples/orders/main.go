// Command orders is the svcrt R0 exemplar: a complete service composed from
// contract, config, and logging with nothing else.
//
// It is deliberately assembled by hand. Every runtime API it touches must be
// pleasant to call from generated code, not just from a human -- that is what
// this file exists to check before the generator is written.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/pavelpascari/svcrt/config"
	"github.com/pavelpascari/svcrt/logging"
)

func main() {
	// The user writes main. Always. Nothing in svcrt calls os.Exit, installs
	// a signal handler, or writes to a global logger -- so the exits below
	// are here, in your code, where you can see them.
	cfg, err := config.Load[AppConfig]()
	if err != nil {
		// The logger is not built yet, so this goes straight to stderr.
		// Every violation is printed at once.
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}

	log := logging.New(os.Stdout, logging.Options{Level: slog.LevelInfo})
	log.Info("starting",
		"addr", cfg.Addr,
		"otel_endpoint", cfg.OTel.Endpoint,
		"db", cfg.DBURL, // Secret: redacts to [REDACTED]
	)

	svc := NewService(map[string]*Order{"1": {ID: "1", Qty: 3}})

	// R1 replaces this with svcrt/httpserver and svcrt/lifecycle.
	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: newServer(svc, log),
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
