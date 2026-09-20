// Command notifications demonstrates an HTTP API and an asynchronous worker
// in one process. Requests retain their trace context while queued, provider
// delivery is retried and circuit-broken, and lifecycle shutdown stops the API
// before draining the queue.
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
	cfg, err := config.Load[AppConfig]()
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}

	log := logging.New(os.Stdout,
		logging.Options{Level: slog.LevelInfo},
		telemetry.LogExtractor(),
	)
	stack := buildStack(stackConfig{
		APIAddr: cfg.APIAddr, AdminAddr: cfg.AdminAddr,
		ProviderURL: cfg.ProviderURL, DrainDelay: cfg.DrainDelay,
		Logger: log,
	})

	ctx, stop := lifecycle.SignalContext(context.Background())
	defer stop()
	if err := stack.lifecycle.Run(ctx); err != nil {
		log.Error("service stopped", "err", err)
		os.Exit(1)
	}
}
