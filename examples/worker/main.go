// Command worker is a background process with no application HTTP surface.
//
// It exists to show that svcrt's module boundaries hold for something that is
// not a web service: lifecycle sequences the consumer, and httpserver appears
// only to answer Kubernetes probes. Even a worker needs those, which is why
// health lives with the HTTP server rather than in its own module.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/pavelpascari/svcrt/config"
	"github.com/pavelpascari/svcrt/lifecycle"
	"github.com/pavelpascari/svcrt/logging"
)

func main() {
	// The user writes main. Always. Nothing in svcrt calls os.Exit, installs a
	// signal handler, or writes to a global logger -- so the exits below are
	// here, in your code, where you can see them.
	cfg, err := config.Load[WorkerConfig]()
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}

	log := logging.New(os.Stdout, logging.Options{Level: slog.LevelInfo})

	s := buildWorkerStack(workerStackConfig{
		AdminAddr:  cfg.AdminAddr,
		DrainDelay: cfg.DrainDelay,
		Logger:     log,
	})

	// Everything below this line is the genuinely untestable part: a real OS
	// signal handler, and the process-level Run loop that blocks until one
	// arrives. go test never calls main, so nothing here is observable by
	// the suite -- which is exactly why buildWorkerStack, including the
	// health flags it sets, lives in stack.go instead of here. This file is
	// the only thing scripts/mutation.sh excludes, and it is now only main().
	ctx, stop := lifecycle.SignalContext(context.Background())
	defer stop()

	if err := s.lc.Run(ctx); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
