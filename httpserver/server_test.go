package httpserver_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/httpserver"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestStartReturnsOnceListeningAndAddrIsResolved(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})
	s := httpserver.New(h, httpserver.Options{Addr: "127.0.0.1:0"})

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	addr := s.Addr()
	if addr == "" || addr == "127.0.0.1:0" {
		t.Fatalf("Addr() = %q, want a resolved host:port", addr)
	}
	// Start returned, so the listener must already accept — no retry loop.
	if code, body := get(t, "http://"+addr); code != 200 || body != "ok" {
		t.Errorf("GET = %d %q, want 200 \"ok\"", code, body)
	}
}

func TestStartOnABusyPortReturnsAnError(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	s := httpserver.New(http.NotFoundHandler(), httpserver.Options{Addr: ln.Addr().String()})
	if err := s.Start(context.Background()); err == nil {
		_ = s.Shutdown(context.Background())
		t.Fatal("Start succeeded on an occupied port")
	}
}

func TestShutdownDrainsAnInFlightRequest(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		fmt.Fprint(w, "finished")
	})
	s := httpserver.New(h, httpserver.Options{Addr: "127.0.0.1:0"})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	type result struct {
		code int
		body string
	}
	res := make(chan result, 1)
	go func() {
		c, b := get(t, "http://"+s.Addr())
		res <- result{c, b}
	}()
	time.Sleep(50 * time.Millisecond) // let the request reach the handler

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- s.Shutdown(context.Background()) }()

	close(release)

	select {
	case r := <-res:
		if r.code != 200 || r.body != "finished" {
			t.Errorf("in-flight request = %d %q, want 200 \"finished\"", r.code, r.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request never completed")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return")
	}
}

// ErrServerClosed is the normal result of Shutdown and must never be reported.
func TestOnServeErrorIsNotCalledOnGracefulShutdown(t *testing.T) {
	t.Parallel()
	var (
		mu     sync.Mutex
		called []error
	)
	s := httpserver.New(http.NotFoundHandler(), httpserver.Options{
		Addr: "127.0.0.1:0",
		OnServeError: func(err error) {
			mu.Lock()
			called = append(called, err)
			mu.Unlock()
		},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(called) != 0 {
		t.Errorf("OnServeError called with %v on a graceful shutdown", called)
	}
}

func TestOnServeErrorFiresWhenTheListenerDies(t *testing.T) {
	t.Parallel()
	got := make(chan error, 1)
	s := httpserver.New(http.NotFoundHandler(), httpserver.Options{
		Addr:         "127.0.0.1:0",
		OnServeError: func(err error) { got <- err },
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	s.CloseListenerForTest() // see Step 3

	select {
	case err := <-got:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			t.Errorf("OnServeError got %v, want a real serve error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnServeError never fired after the listener closed")
	}
}

// An unset ReadHeaderTimeout is a Slowloris hole; it must never be zero.
func TestReadHeaderTimeoutIsNeverZero(t *testing.T) {
	t.Parallel()
	s := httpserver.New(http.NotFoundHandler(), httpserver.Options{Addr: "127.0.0.1:0"})
	if d := s.ReadHeaderTimeoutForTest(); d <= 0 {
		t.Errorf("ReadHeaderTimeout = %v, want a non-zero default", d)
	}
}

func TestExplicitReadHeaderTimeoutIsKept(t *testing.T) {
	t.Parallel()
	s := httpserver.New(http.NotFoundHandler(), httpserver.Options{
		Addr: "127.0.0.1:0", ReadHeaderTimeout: 3 * time.Second,
	})
	if d := s.ReadHeaderTimeoutForTest(); d != 3*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 3s", d)
	}
}

func TestAddrBeforeStartIsTheConfiguredValue(t *testing.T) {
	t.Parallel()
	s := httpserver.New(http.NotFoundHandler(), httpserver.Options{Addr: "127.0.0.1:8080"})
	if got := s.Addr(); got != "127.0.0.1:8080" {
		t.Errorf("Addr() before Start = %q, want the configured address", got)
	}
}

func TestStartWithAPreCancelledContextAbortsTheBind(t *testing.T) {
	t.Parallel()
	s := httpserver.New(http.NotFoundHandler(), httpserver.Options{Addr: "127.0.0.1:0"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.Start(ctx); err == nil {
		_ = s.Shutdown(context.Background())
		t.Fatal("Start succeeded with a pre-cancelled context")
	}
	if addr := s.Addr(); addr != "127.0.0.1:0" {
		t.Errorf("Addr() = %q after a failed Start, want the unresolved configured address", addr)
	}
}

func TestShutdownBeforeStartIsANoOp(t *testing.T) {
	t.Parallel()
	s := httpserver.New(http.NotFoundHandler(), httpserver.Options{Addr: "127.0.0.1:0"})
	if err := s.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown before Start = %v, want nil", err)
	}
}
