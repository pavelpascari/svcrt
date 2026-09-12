// Package lifecycle sequences a process's startup and shutdown.
//
// A component's Start returns when the component is READY, not when it is
// finished; long-running work belongs in a goroutine the component owns. That
// is what makes a dependency edge meaningful — an edge waits on readiness, and
// a Start that blocked for the process lifetime could never be depended upon.
//
// lifecycle imports no other svcrt module and no net/http, so it is usable
// from a CLI, a one-shot job, or a worker with no HTTP surface at all.
package lifecycle

import (
	"context"
	"time"
)

// Ref, Option, After, and StopTimeout are declared in lifecycle.go (Task 2),
// because Ref carries a *Lifecycle and that type does not exist yet.

// StartFunc returns when the component is ready, or returns an error.
//
// It must honour ctx cancellation while starting, and on returning an error it
// must leave nothing behind: Stop is never called for a component whose Start
// did not return nil.
type StartFunc func(context.Context) error

// StopFunc drains the component. It receives a context carrying the caller's
// values but not their cancellation (see Run), bounded by the component's
// StopTimeout.
type StopFunc func(context.Context) error

type component struct {
	name  string
	start StartFunc
	stop  StopFunc

	// afterRefs is what the caller declared; after is the resolved index list,
	// filled by Add once it has validated that each Ref belongs to it.
	afterRefs []Ref
	after     []int

	stopTimeout time.Duration // 0 means use Config.StopTimeout
}
