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

	mu     sync.Mutex
	checks []readyCheck
}

// Set opens or closes the gate. Safe from any goroutine.
func (g *Gate) Set(ok bool) { g.open.Store(ok) }

// ServeHTTP answers 200 when the gate is open and every check passes, and 503
// otherwise. A failing response names the checks that failed and nothing else:
// the check's error text is the caller's to log, never to serialize.
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

// NewHealth returns a Health with all three gates closed.
func NewHealth() *Health {
	return &Health{Started: &Gate{}, Live: &Gate{}, Ready: &Gate{}}
}

// AddReadyCheck registers a dependency check on the READINESS gate.
//
// There is deliberately no AddLiveCheck. A liveness probe that fails when the
// database is unreachable makes Kubernetes restart a perfectly healthy process
// during a database outage, turning a degraded service into a crash-loop across
// every replica at once. Making that unrepresentable is the entire reason this
// type exists rather than three bare atomic.Bools.
func (h *Health) AddReadyCheck(name string, fn func(context.Context) error) {
	h.Ready.mu.Lock()
	defer h.Ready.mu.Unlock()
	h.Ready.checks = append(h.Ready.checks, readyCheck{name: name, fn: fn})
}

// Drain closes the readiness gate. It is the hook to register with a
// lifecycle's OnDrain, as a method value:
//
//	lc.OnDrain(h.Drain)
//
// Live and Started are deliberately untouched: the process is not wedged and
// did boot, and a liveness failure mid-drain invites a restart.
func (h *Health) Drain() { h.Ready.Set(false) }
