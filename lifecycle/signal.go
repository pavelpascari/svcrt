package lifecycle

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// SignalContext returns a context cancelled on the first SIGINT or SIGTERM.
//
// After the first signal it restores default signal handling, so a second
// Ctrl-C — or a Kubernetes SIGKILL chaser — terminates the process. lifecycle
// still never calls os.Exit; the operating system does. A drain that ignores
// the second signal is the classic way a pod hangs until its termination grace
// period expires.
//
// The returned stop function is idempotent and safe to defer.
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop() // restore default handling for the next signal
	}()
	return ctx, stop
}
