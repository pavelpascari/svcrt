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
	"github.com/pavelpascari/svcrt/lifecycle"
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
	lc       *lifecycle.Lifecycle
	health   *httpserver.Health
	api      *httpserver.Server
	admin    *httpserver.Server
	mu       sync.Mutex
	ops      []string
	slowGate chan struct{}
}

func newStack(t *testing.T, drainDelay time.Duration) *stack {
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
		Trace: func(op string) {
			s.mu.Lock()
			s.ops = append(s.ops, op)
			s.mu.Unlock()
		},
	})
	s.lc = built.lc
	s.health = built.health
	s.api = built.api
	s.admin = built.admin

	return s
}

func (s *stack) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ops)
}

func TestAcceptanceStartsInDependencyOrder(t *testing.T) {
	s := newStack(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	ops := s.snapshot()
	storeAt := slices.Index(ops, "start:store")
	apiAt := slices.Index(ops, "start:api")
	if storeAt < 0 || apiAt < 0 || storeAt > apiAt {
		t.Errorf("ops = %v, want store started before api", ops)
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
	s := newStack(t, 0)
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
	s := newStack(t, 300*time.Millisecond)
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
	storeStop := slices.Index(ops, "stop:store")
	if apiStop < 0 || storeStop < 0 || apiStop > storeStop {
		t.Errorf("ops = %v, want api stopped before store (reverse order)", ops)
	}
}

func TestAcceptanceFatalTriggersTheSameDrain(t *testing.T) {
	s := newStack(t, 0)
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
