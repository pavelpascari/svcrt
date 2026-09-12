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
	"time"

	"github.com/pavelpascari/svcrt/config"
	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/lifecycle"
	"github.com/pavelpascari/svcrt/logging"
)

// workerStack is the health/lifecycle/queue/admin composition. It is built by
// buildWorkerStack, the one function main and the tests both call -- so a
// regression in this wiring (a dropped OnDrain, a missing After, a forgotten
// health flag) fails in the test binary instead of only being visible in a
// running process.
type workerStack struct {
	lc       *lifecycle.Lifecycle
	health   *httpserver.Health
	admin    *httpserver.Server
	consumer *Consumer
}

// workerStackConfig carries what a test wiring must supply differently from
// production: Trace is optional instrumentation production leaves nil, used
// by tests to record start/stop order without re-implementing this wiring.
type workerStackConfig struct {
	AdminAddr  string
	DrainDelay time.Duration
	Logger     *slog.Logger
	Trace      func(op string)
}

// buildWorkerStack wires health, lifecycle, the consumer, and the admin
// server exactly as main runs them. Both main and the tests call this -- it
// is the seam that makes the drain and dependency-order behavior observable
// from a test rather than only from a running binary.
//
// The returned stack comes up ready: Started, Live, and Ready are all set
// true before this function returns, unconditionally, for both callers. That
// flag-setting used to live in main as three lines a future edit could
// silently drop -- a dropped Set(true) fails with no crash and no log line,
// only every readiness probe returning 503 forever. Moving it here removes
// the line from main that could be forgotten.
func buildWorkerStack(cfg workerStackConfig) *workerStack {
	trace := cfg.Trace
	if trace == nil {
		trace = func(string) {}
	}

	health := httpserver.NewHealth()
	lc := lifecycle.New(lifecycle.Config{DrainDelay: cfg.DrainDelay, Logger: cfg.Logger})

	// The whole health/lifecycle seam, in one visible line. Neither module
	// imports the other; the coupling lives here, in your code.
	lc.OnDrain(health.Drain)

	c := NewConsumer()
	queue := lc.Add("queue",
		func(ctx context.Context) error { trace("start:queue"); return c.Start(ctx) },
		func(ctx context.Context) error { trace("stop:queue"); return c.Stop(ctx) })

	// The admin server depends on the consumer, so readiness only becomes
	// reachable once work is actually being processed -- and stops first on
	// the way out.
	admin := httpserver.New(httpserver.AdminMux(health), httpserver.Options{
		Addr: cfg.AdminAddr, OnServeError: lc.Fatal,
	})
	lc.Add("admin",
		func(ctx context.Context) error { trace("start:admin"); return admin.Start(ctx) },
		func(ctx context.Context) error { trace("stop:admin"); return admin.Shutdown(ctx) },
		lifecycle.After(queue))

	health.Started.Set(true)
	health.Live.Set(true)
	health.Ready.Set(true)

	return &workerStack{lc: lc, health: health, admin: admin, consumer: c}
}

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
	// the suite -- which is exactly why the wiring above, including the
	// health flags buildWorkerStack sets, was extracted instead of living
	// here.
	ctx, stop := lifecycle.SignalContext(context.Background())
	defer stop()

	if err := s.lc.Run(ctx); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
