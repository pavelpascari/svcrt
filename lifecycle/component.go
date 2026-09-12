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
	"slices"
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

	// after is the resolved dependency index list. Task 2 adds the afterRefs
	// field alongside it, once Ref exists.
	after []int

	stopTimeout time.Duration // 0 means use Config.StopTimeout
}

// levels groups components into start levels: every component in level n
// depends only on components in levels < n, so a level may start concurrently.
//
// A single forward pass suffices — no topological sort — because a Ref can only
// name an earlier component, so every dependency index is less than its
// dependent's. That is a direct consequence of the handle-based edge design.
func levels(cs []component) [][]int {
	if len(cs) == 0 {
		return nil
	}
	lvl := make([]int, len(cs))
	depth := 0
	for i, c := range cs {
		l := 0
		for _, d := range c.after {
			if lvl[d]+1 > l {
				l = lvl[d] + 1
			}
		}
		lvl[i] = l
		depth = max(depth, l)
	}
	out := make([][]int, depth+1)
	for i := range cs {
		out[lvl[i]] = append(out[lvl[i]], i)
	}
	return slices.Clip(out)
}
