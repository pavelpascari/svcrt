package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// checkTimeout bounds each readiness check so a hung dependency cannot hang
// the probe itself.
const checkTimeout = 2 * time.Second

type readyCheck struct {
	name string
	fn   func(context.Context) error
}

// Gate is a settable health probe. Its zero state is closed (503).
type Gate struct {
	open atomic.Bool

	// OnCheckError is called when a readiness check fails, with the check's
	// name and its error. Nil means the cause is discarded.
	//
	// It exists because the error must not reach the wire (a 503 body carries
	// check names, never prose) but must reach somewhere: wire this to your
	// logger in main.
	//
	// SET IT BEFORE THIS GATE IS SERVED, AND DO NOT CHANGE IT AFTERWARDS.
	// Unlike open and checks, this field is a plain field with no
	// synchronization, and ServeHTTP reads it from every request goroutine --
	// so assigning it once the admin server is accepting is a data race. It
	// is deliberately not an atomic or a mutex-guarded setter: a logger
	// swapped mid-flight is not a thing services need, and the cost of
	// pretending otherwise is an atomic load on every failing check plus an
	// API that implies a capability nobody should use. The one supported
	// order is construct, wire, then serve.
	OnCheckError func(name string, err error)

	mu     sync.Mutex
	checks []readyCheck
}

// Set opens or closes the gate. Safe from any goroutine.
func (g *Gate) Set(ok bool) { g.open.Store(ok) }

// ServeHTTP answers 200 when the gate is open and every check passes, and 503
// otherwise. A failing response names the checks that failed and nothing else:
// the check's error is reported via OnCheckError (if set), never serialized.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.open.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	g.mu.Lock()
	checks := make([]readyCheck, len(g.checks))
	copy(checks, g.checks)
	g.mu.Unlock()

	var failed []string
	for _, c := range checks {
		ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
		err := c.fn(ctx)
		cancel()
		if err != nil {
			if g.OnCheckError != nil {
				g.OnCheckError(c.name, err)
			}
			failed = append(failed, c.name)
		}
	}

	if len(failed) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(struct {
		Failed []string `json:"failed"`
	}{failed})
}

// Health holds the three probes Kubernetes distinguishes.
//
// They are separate because conflating them causes real outages:
//
//   - Started: has boot completed.
//   - Live:    is the process wedged. DEPENDENCY-INDEPENDENT, deliberately.
//   - Ready:   can we serve now. Dependency-gated; false during drain.
type Health struct {
	Started *Gate
	Live    *Gate
	Ready   *Gate
}

// NewHealth returns a Health with Started and Ready closed and Live already
// open.
//
// Live is the odd one out on purpose. A failing liveness probe asks the
// orchestrator to RESTART the process, so a Live that stays closed until boot
// finishes turns a slow start into a restart loop — the process is killed for
// not having finished starting, then killed again, forever. That is the exact
// failure a startupProbe exists to prevent, and Started is the gate that
// answers it. Live claims only that the process is not wedged, which is true
// from the moment it can answer a request at all.
//
// Started and Ready are opened by Up once the service has actually started.
func NewHealth() *Health {
	h := &Health{Started: &Gate{}, Live: &Gate{}, Ready: &Gate{}}
	h.Live.Set(true)
	return h
}

// AddReadyCheck registers a dependency check on the READINESS gate.
//
// There is deliberately no AddLiveCheck: no exported API attaches a
// dependency check to Live or Started, and Gate's checks field is
// unexported, so a Gate carrying checks cannot be constructed from outside
// this package. That is what is actually prevented — a caller cannot reach
// for an AddLiveCheck that does not exist, or smuggle checks onto Live by
// any means this package exposes.
//
// What is NOT prevented: Health.Live, .Ready, and .Started are exported
// *Gate fields — they have to be, so main can mount them and call Set — and
// nothing stops a caller from writing h.Live = h.Ready, aliasing liveness
// onto a dependency-gated probe and defeating the separation this type
// exists for. That is not an accident this type can catch; it is a caller
// rewriting their own wiring. Don't do it.
func (h *Health) AddReadyCheck(name string, fn func(context.Context) error) {
	h.Ready.mu.Lock()
	defer h.Ready.mu.Unlock()
	h.Ready.checks = append(h.Ready.checks, readyCheck{name: name, fn: fn})
}

// Up opens the startup and readiness gates. It is the hook to register with a
// lifecycle's OnStarted, as a method value:
//
//	lc.OnStarted(h.Up)
//
// Registering it there rather than calling it at construction time is what
// makes readiness honest: the gates open when the last component has finished
// starting, not before the first one has. Opened by hand while wiring, they
// answer 200 during startup, so a probe can see a service ready while a
// listener it depends on is still binding.
//
// Live is untouched because NewHealth already opened it; see there for why.
func (h *Health) Up() {
	h.Started.Set(true)
	h.Ready.Set(true)
}

// Drain closes the readiness gate. It is the hook to register with a
// lifecycle's OnDrain, as a method value:
//
//	lc.OnDrain(h.Drain)
//
// Live and Started are deliberately untouched: the process is not wedged and
// did boot, and a liveness failure mid-drain invites a restart.
func (h *Health) Drain() { h.Ready.Set(false) }
