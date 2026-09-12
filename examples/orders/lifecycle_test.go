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

// stack builds the same wiring main() does, with addresses on :0 and a
// recorder around the store so start/stop order is observable.
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
	s.health = httpserver.NewHealth()
	s.lc = lifecycle.New(lifecycle.Config{DrainDelay: drainDelay})
	s.lc.OnDrain(s.health.Drain)

	record := func(op string) {
		s.mu.Lock()
		s.ops = append(s.ops, op)
		s.mu.Unlock()
	}

	store := s.lc.Add("store",
		func(context.Context) error { record("start:store"); return nil },
		func(context.Context) error { record("stop:store"); return nil })

	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		<-s.slowGate
		fmt.Fprint(w, "finished")
	})

	s.api = httpserver.New(mux, httpserver.Options{
		Addr: "127.0.0.1:0", OnServeError: s.lc.Fatal,
	})
	s.lc.Add("api",
		func(ctx context.Context) error { record("start:api"); return s.api.Start(ctx) },
		func(ctx context.Context) error { record("stop:api"); return s.api.Shutdown(ctx) },
		lifecycle.After(store))

	s.admin = httpserver.New(httpserver.AdminMux(s.health), httpserver.Options{
		Addr: "127.0.0.1:0", OnServeError: s.lc.Fatal,
	})
	s.lc.Add("admin",
		func(ctx context.Context) error { record("start:admin"); return s.admin.Start(ctx) },
		func(ctx context.Context) error { record("stop:admin"); return s.admin.Shutdown(ctx) },
		lifecycle.After(store))

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

	s.health.Started.Set(true)
	s.health.Ready.Set(true)
	s.health.Live.Set(true)

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

func TestAcceptanceProbesAnswerAfterBoot(t *testing.T) {
	s := newStack(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	s.health.Started.Set(true)
	s.health.Ready.Set(true)
	s.health.Live.Set(true)

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

	s.health.Started.Set(true)
	s.health.Ready.Set(true)
	s.health.Live.Set(true)

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
	s.health.Ready.Set(true)

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
