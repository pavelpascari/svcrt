package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/httpserver"
)

func status(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

// stack is a thin wrapper around buildStack's result: it adds the ops
// recorder and the /slow endpoint the acceptance tests need but production
// never serves. The wiring itself -- health, lifecycle, api, admin -- comes
// from buildStack, the same function main() calls, so a regression in that
// wiring is visible here rather than only in a running process.
type stack struct {
	// Embedded rather than re-declared: lc, health, api, admin and pricing
	// were listed here and copied across one by one, which is five field
	// declarations and five assignments that say nothing. Promotion keeps
	// s.lc and s.admin reading identically at every call site.
	*appStack

	mu       sync.Mutex
	ops      []string
	slowGate chan struct{}
}

func newStack(t *testing.T, drainDelay time.Duration, pricingURL string) *stack {
	t.Helper()
	s := &stack{slowGate: make(chan struct{})}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		<-s.slowGate
		fmt.Fprint(w, "finished")
	})

	built := buildStack(appStackConfig{
		APIAddr:    "127.0.0.1:0",
		AdminAddr:  "127.0.0.1:0",
		DrainDelay: drainDelay,
		Handler:    mux,
		StoreOpen:  func(context.Context) error { return nil },
		StoreClose: func(context.Context) error { return nil },
		PricingURL: pricingURL,
		Trace: func(op string) {
			s.mu.Lock()
			s.ops = append(s.ops, op)
			s.mu.Unlock()
		},
	})
	s.appStack = built

	return s
}

func (s *stack) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ops)
}

func TestAcceptanceStartsInDependencyOrder(t *testing.T) {
	s := newStack(t, 0, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	// Both dependents, not just the api: admin is After(store) too, and
	// nothing else in this suite observes that edge -- moving buildStack into
	// stack.go, where the mutation gate can see it, showed the admin trace
	// calls surviving deletion for exactly that reason.
	ops := s.snapshot()
	storeAt := slices.Index(ops, "start:store")
	apiAt := slices.Index(ops, "start:api")
	adminAt := slices.Index(ops, "start:admin")
	if storeAt < 0 || apiAt < 0 || storeAt > apiAt {
		t.Errorf("ops = %v, want store started before api", ops)
	}
	if adminAt < 0 || storeAt > adminAt {
		t.Errorf("ops = %v, want store started before admin", ops)
	}

	close(s.slowGate)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

// buildStack brings the stack up ready with no help from the caller -- this
// asserts that default directly, rather than the test priming it with its
// own Set(true) calls first.
func TestAcceptanceProbesAnswerAfterBoot(t *testing.T) {
	s := newStack(t, 0, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	base := "http://" + s.admin.Addr()
	for _, p := range []string{"/startupz", "/readyz", "/healthz"} {
		if code := status(t, base+p); code != 200 {
			t.Errorf("GET %s = %d, want 200", p, code)
		}
	}

	close(s.slowGate)
	cancel()
	<-done
}

// THE acceptance criterion: readiness goes 503 before the API stops accepting,
// and a request in flight when the signal arrives still completes.
func TestAcceptanceDrainFlipsReadinessBeforeStoppingAndCompletesInFlight(t *testing.T) {
	s := newStack(t, 300*time.Millisecond, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	adminBase := "http://" + s.admin.Addr()

	// Put a request in flight and leave it there.
	type res struct {
		code int
		body string
	}
	inflight := make(chan res, 1)
	go func() {
		resp, err := http.Get("http://" + s.api.Addr() + "/slow")
		if err != nil {
			inflight <- res{0, err.Error()}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		inflight <- res{resp.StatusCode, string(b)}
	}()
	time.Sleep(100 * time.Millisecond)

	cancel() // the signal

	// During DrainDelay readiness must already be false while the API is
	// still accepting.
	time.Sleep(100 * time.Millisecond)
	if code := status(t, adminBase+"/readyz"); code != 503 {
		t.Errorf("readiness during drain = %d, want 503", code)
	}
	if code := status(t, adminBase+"/healthz"); code != 200 {
		t.Errorf("liveness during drain = %d, want 200 (the process is not wedged)", code)
	}

	close(s.slowGate) // let the in-flight request finish

	select {
	case r := <-inflight:
		if r.code != 200 || r.body != "finished" {
			t.Errorf("in-flight request = %d %q, want 200 \"finished\"", r.code, r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the in-flight request never completed")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}

	ops := s.snapshot()
	apiStop := slices.Index(ops, "stop:api")
	adminStop := slices.Index(ops, "stop:admin")
	storeStop := slices.Index(ops, "stop:store")
	if apiStop < 0 || storeStop < 0 || apiStop > storeStop {
		t.Errorf("ops = %v, want api stopped before store (reverse order)", ops)
	}
	if adminStop < 0 || adminStop > storeStop {
		t.Errorf("ops = %v, want admin stopped before store (reverse order)", ops)
	}
}

func TestAcceptanceFatalTriggersTheSameDrain(t *testing.T) {
	s := newStack(t, 0, "")
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(context.Background()) }()
	time.Sleep(100 * time.Millisecond)

	close(s.slowGate)
	s.lc.Fatal(fmt.Errorf("listener died"))

	select {
	case err := <-done:
		if err == nil {
			t.Error("Run = nil, want the fatal error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Fatal did not wake Run")
	}

	ops := s.snapshot()
	if !slices.Contains(ops, "stop:store") {
		t.Errorf("ops = %v, want an ordered shutdown after Fatal", ops)
	}
}

// buildStack's Trace parameter is optional -- production (main) never sets
// it. This exercises that default path directly, since none of the
// acceptance tests above (which all supply a Trace) reach it.
func TestBuildStackDefaultsTraceToANoop(t *testing.T) {
	t.Parallel()

	built := buildStack(appStackConfig{
		APIAddr:    "127.0.0.1:0",
		AdminAddr:  "127.0.0.1:0",
		Handler:    http.NewServeMux(),
		StoreOpen:  func(context.Context) error { return nil },
		StoreClose: func(context.Context) error { return nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- built.lc.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestAcceptanceADeadListenerTriggersAnOrderedShutdown is the test
// TestAcceptanceFatalTriggersTheSameDrain only looks like: that one calls
// lc.Fatal directly, so the server->lifecycle edge -- `OnServeError:
// lc.Fatal` in buildStack -- is never exercised and deleting it leaves the
// suite green. This kills the listener out from under Serve instead, which
// is the production failure (fd exhaustion, an accept storm) that field
// exists for. Without it the process stays up with every probe answering
// 200 and nothing serving, which is precisely the outage lifecycle.Fatal was
// designed to prevent.
//
// Both servers are covered: each one's OnServeError is a separate line that
// can be dropped on its own.
func TestAcceptanceADeadListenerTriggersAnOrderedShutdown(t *testing.T) {
	for _, tc := range []struct {
		name string
		kill func(*stack) *httpserver.Server
	}{
		{"api", func(s *stack) *httpserver.Server { return s.api }},
		{"admin", func(s *stack) *httpserver.Server { return s.admin }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, 0, "")
			done := make(chan error, 1)
			go func() { done <- s.lc.Run(context.Background()) }()
			time.Sleep(100 * time.Millisecond)

			close(s.slowGate)
			if err := tc.kill(s).CloseListener(); err != nil {
				t.Fatalf("CloseListener: %v", err)
			}

			select {
			case err := <-done:
				if err == nil {
					t.Error("Run = nil; a dead listener did not reach lifecycle.Fatal -- check OnServeError in buildStack")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("a dead listener never woke Run -- check OnServeError in buildStack")
			}

			if ops := s.snapshot(); !slices.Contains(ops, "stop:store") {
				t.Errorf("ops = %v, want an ordered shutdown after the listener died", ops)
			}
		})
	}
}
