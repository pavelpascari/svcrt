package main

import (
	"context"
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

// stack is a thin wrapper around buildWorkerStack's result: it adds the ops
// recorder the acceptance tests need but production never sets. The wiring
// itself -- health, lifecycle, queue, admin -- comes from buildWorkerStack,
// the same function main() calls, so a regression in that wiring is visible
// here rather than only in a running process.
type stack struct {
	lc     *lifecycle.Lifecycle
	health *httpserver.Health
	admin  *httpserver.Server

	mu  sync.Mutex
	ops []string
}

func newStack(t *testing.T, drainDelay time.Duration) *stack {
	t.Helper()
	s := &stack{}

	built := buildWorkerStack(workerStackConfig{
		AdminAddr:  "127.0.0.1:0",
		DrainDelay: drainDelay,
		Trace: func(op string) {
			s.mu.Lock()
			s.ops = append(s.ops, op)
			s.mu.Unlock()
		},
	})
	s.lc = built.lc
	s.health = built.health
	s.admin = built.admin

	return s
}

func (s *stack) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ops)
}

// TestWorkerServesProbesWithNoApplicationSurface is the acceptance criterion
// for this exemplar: the admin server depends on the queue (lifecycle.After),
// so readiness only becomes reachable once the consumer is running -- and the
// admin stops first on the way out.
func TestWorkerServesProbesWithNoApplicationSurface(t *testing.T) {
	s := newStack(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	base := "http://" + s.admin.Addr()
	if code := status(t, base+"/readyz"); code != 200 {
		t.Errorf("readiness = %d, want 200", code)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}

	ops := s.snapshot()
	qStart := slices.Index(ops, "start:queue")
	aStart := slices.Index(ops, "start:admin")
	aStop := slices.Index(ops, "stop:admin")
	qStop := slices.Index(ops, "stop:queue")
	if qStart < 0 || aStart < 0 || qStart > aStart {
		t.Errorf("ops = %v, want queue to start before admin", ops)
	}
	if aStop < 0 || qStop < 0 || aStop > qStop {
		t.Errorf("ops = %v, want admin to stop before queue", ops)
	}
}

// buildWorkerStack brings the stack up ready with no help from the caller --
// this asserts that default directly, rather than the test priming it with
// its own Set(true) calls first.
func TestWorkerProbesAnswerAfterBoot(t *testing.T) {
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

// TestWorkerDrainFlipsReadiness exercises OnDrain: readiness must go 503
// during the drain delay while liveness stays 200 (the process is not
// wedged).
func TestWorkerDrainFlipsReadiness(t *testing.T) {
	s := newStack(t, 300*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	base := "http://" + s.admin.Addr()
	cancel()

	time.Sleep(100 * time.Millisecond)
	switch code := status(t, base+"/readyz"); code {
	case 503:
		// want
	case 0:
		t.Errorf("readiness during drain: could not reach %s at all -- the admin server has already stopped, not just gone unready. That means the drain delay isn't being honored (e.g. DrainDelay wired to zero), not a network flake.", base+"/readyz")
	default:
		t.Errorf("readiness during drain = %d, want 503", code)
	}
	if code := status(t, base+"/healthz"); code != 200 {
		t.Errorf("liveness during drain = %d, want 200 (the process is not wedged)", code)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

// buildWorkerStack's Trace parameter is optional -- production (main) never
// sets it. This exercises that default path directly, since none of the
// acceptance tests above (which all supply a Trace) reach it.
func TestBuildWorkerStackDefaultsTraceToANoop(t *testing.T) {
	t.Parallel()

	built := buildWorkerStack(workerStackConfig{
		AdminAddr: "127.0.0.1:0",
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

func TestConsumerStopsProcessing(t *testing.T) {
	t.Parallel()
	c := NewConsumer()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !c.Running() {
		t.Error("consumer is not running after Start")
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Running() {
		t.Error("consumer still running after Stop")
	}
}

func TestConsumerStopIsIdempotent(t *testing.T) {
	t.Parallel()
	c := NewConsumer()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Errorf("second Stop = %v, want nil", err)
	}
	// The second Stop must take the already-stopped early-return branch and
	// actually release the lock there, not fall through and leave it held.
	// TryLock is non-blocking, so a broken guard fails this test instead of
	// wedging every later test that touches c.mu.
	if !c.mu.TryLock() {
		t.Fatal("second Stop returned with its mutex still held")
	}
	c.mu.Unlock()
}

// TestConsumerStopWithoutStartIsANoop covers a case the idempotent-stop test
// above does not: a Consumer that was never Start()'d has a nil cancel and a
// nil done channel, unlike one that has been through a Start/Stop cycle. The
// early-return guard has to hold on its own here, not just work by accident
// because leftover state from a prior Start happens to make the fallthrough
// harmless.
func TestConsumerStopWithoutStartIsANoop(t *testing.T) {
	t.Parallel()
	c := NewConsumer()
	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on a never-started consumer: %v", err)
	}
	if !c.mu.TryLock() {
		t.Fatal("Stop on a never-started consumer returned with its mutex still held")
	}
	c.mu.Unlock()
}

func TestConsumerStartIsIdempotent(t *testing.T) {
	t.Parallel()
	c := NewConsumer()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstDone := c.done
	if err := c.Start(context.Background()); err != nil {
		t.Errorf("second Start = %v, want nil", err)
	}
	// A second Start while already running must be a true no-op: it must not
	// replace the running goroutine's done channel, which is exactly what
	// happens if the "already running" guard is skipped -- the old goroutine
	// is orphaned and Stop only ever waits on the new one.
	if c.done != firstDone {
		t.Error("second Start replaced the running consumer's internal state instead of no-op'ing")
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConsumerStopRespectsContextDeadline(t *testing.T) {
	t.Parallel()
	c := NewConsumer()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Stop(ctx); err == nil {
		t.Error("Stop with an already-cancelled context = nil, want an error")
	}
}
