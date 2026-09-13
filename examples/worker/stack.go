package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/lifecycle"
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
// The returned stack is NOT yet ready: Started and Ready open when Run has
// finished starting every component, via the OnStarted hook below. Live is
// already open, because NewHealth opens it -- a liveness gate that waits for
// boot restarts a slow-starting process.
//
// Those gates used to be three Set(true) calls at the end of this function,
// which was easy to drop silently -- unlike a missing OnDrain, a missing
// Set(true) fails with no crash and no log line, only every readiness probe
// returning 503 forever. Handing both edges to the lifecycle puts them next
// to each other and lets the thing that did the starting decide when
// starting is done.
func buildWorkerStack(cfg workerStackConfig) *workerStack {
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

	return &workerStack{lc: lc, health: health, admin: admin, consumer: c}
}
