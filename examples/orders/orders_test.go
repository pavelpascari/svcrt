package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/config"
	"github.com/pavelpascari/svcrt/contract"
)

func mapSource(m map[string]string) config.Source {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestAppConfigLoadsWithMinimalEnvironment(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load[AppConfig](config.WithSource(mapSource(map[string]string{
		"DATABASE_URL": "postgres://localhost/orders",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.OTel.Timeout != 5*time.Second {
		t.Errorf("OTel.Timeout = %v, want 5s", cfg.OTel.Timeout)
	}
	if cfg.TLS != nil {
		t.Errorf("TLS = %+v, want nil", cfg.TLS)
	}
	if string(cfg.DBURL) != "postgres://localhost/orders" {
		t.Errorf("DBURL = %q", string(cfg.DBURL))
	}
}

func TestAppConfigRequiresDatabaseURL(t *testing.T) {
	t.Parallel()

	if _, err := config.Load[AppConfig](config.WithSource(mapSource(nil))); err == nil {
		t.Fatal("Load succeeded without DATABASE_URL")
	}
}

// openStore returns a Store already open, for tests that do not run a lifecycle.
func openStore(orders map[string]*Order) *Store {
	s := NewStore(orders)
	_ = s.Open(context.Background())
	return s
}

func TestServiceReturnsOrder(t *testing.T) {
	t.Parallel()

	svc := NewService(openStore(map[string]*Order{"1": {ID: "1", Qty: 3}}))

	got, err := svc.GetOrder(context.Background(), GetOrderRequest{ID: "1"})
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Qty != 3 {
		t.Errorf("Qty = %d, want 3", got.Qty)
	}
}

func TestServiceReturnsCodedNotFound(t *testing.T) {
	t.Parallel()

	svc := NewService(openStore(nil))

	_, err := svc.GetOrder(context.Background(), GetOrderRequest{ID: "missing"})
	if err == nil {
		t.Fatal("GetOrder succeeded for a missing order")
	}

	var c contract.Coded
	if !errors.As(err, &c) {
		t.Fatalf("err %T does not satisfy contract.Coded", err)
	}
	if c.ErrorCode() != "order_not_found" {
		t.Errorf("code = %q, want order_not_found", c.ErrorCode())
	}

	// No params on this one -- it must not accidentally satisfy Detailed.
	var d contract.Detailed
	if errors.As(err, &d) {
		t.Error("a paramless error satisfied contract.Detailed")
	}
}

func TestIDTooLongErrorCarriesScalarParams(t *testing.T) {
	t.Parallel()

	var err error = idTooLongError{Max: 8, Got: 40}

	var d contract.Detailed
	if !errors.As(err, &d) {
		t.Fatalf("err %T does not satisfy contract.Detailed", err)
	}
	if d.ErrorCode() != "id_too_long" {
		t.Errorf("code = %q, want id_too_long", d.ErrorCode())
	}

	params := d.ErrorParams()
	if params["max"] != 8 || params["got"] != 40 {
		t.Errorf("params = %v, want max=8 got=40", params)
	}

	// Params must be scalars so a client can substitute them into its own
	// template. Anything else is unrenderable.
	for k, v := range params {
		switch v.(type) {
		case string, int, int64, float64, bool:
		default:
			t.Errorf("param %q is %T; params must be scalars", k, v)
		}
	}
}

func TestNotFoundErrorMessageNamesTheID(t *testing.T) {
	t.Parallel()

	err := notFoundError{id: "abc123"}

	// The message must name which order was missing -- this string lands in
	// server-side logs, and "not found" with no ID is useless to an on-call
	// engineer.
	if !strings.Contains(err.Error(), "abc123") {
		t.Errorf("Error() = %q, want it to contain the order id %q", err.Error(), "abc123")
	}
}

func TestIDTooLongErrorMessagePreservesArgumentOrder(t *testing.T) {
	t.Parallel()

	err := idTooLongError{Max: 8, Got: 40}

	// Pin the exact string, not just presence of the digits: "40" and "8"
	// both appear whichever way Got/Max are swapped in the Sprintf call, so
	// only whole-string equality catches a future argument-order regression.
	const want = "id length 40 exceeds max 8"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// NewStore(nil) must substitute an empty map, not keep the nil. Reads from a
// nil map are harmless, so this is invisible through Get -- but a write
// panics, and the guard exists for the moment this store grows one.
// Per this project's dead-code precedent: a clause whose difference some
// caller can observe is kept and tested, not deleted.
func TestNewStoreReplacesANilMap(t *testing.T) {
	t.Parallel()

	s := NewStore(nil)

	if s.orders == nil {
		t.Fatal("NewStore(nil) left the map nil")
	}
	// The observable consequence: this line panics on a nil map.
	s.orders["1"] = &Order{ID: "1", Qty: 1}
	_ = s.Open(context.Background())

	got, ok := s.Get("1")
	if !ok {
		t.Fatal("Get(1) after a write reported not-found")
	}
	if got.Qty != 1 {
		t.Errorf("Qty = %d, want 1", got.Qty)
	}
}

// Store.Get reports not-found once the store is closed, so a request
// arriving after shutdown cannot read stale data.
func TestStoreGetReturnsNotFoundAfterClose(t *testing.T) {
	t.Parallel()

	s := openStore(map[string]*Order{"1": {ID: "1", Qty: 3}})

	if _, ok := s.Get("1"); !ok {
		t.Fatal("Get(1) reported not-found while the store is open")
	}

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, ok := s.Get("1"); ok {
		t.Error("Get(1) succeeded after Close; want not-found")
	}
}

// TestStoreGetIsSafeConcurrentlyWithClose pins the reason Store.open is
// atomic. Get's doc promises a request arriving after shutdown reads no stale
// data, which is a statement about Get and Close running at the same time --
// and the exemplar is teaching material, so the code has to make that claim
// true rather than rely on the dependency edge happening to serialize them.
// It does not always: api is After(store), so api.Shutdown normally drains
// before store.Close, but a Shutdown that returns on its deadline instead
// (a handler slower than StopTimeout, or a hijacked connection) leaves Close
// writing while Get reads. With a plain bool this fails under -race.
//
// The acceptance suites cannot catch this: they substitute their own
// StoreOpen/StoreClose and never touch the real Store.
func TestStoreGetIsSafeConcurrentlyWithClose(t *testing.T) {
	t.Parallel()
	st := NewStore(map[string]*Order{"1": {ID: "1", Qty: 3}})
	if err := st.Open(context.Background()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			st.Get("1")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			if err := st.Close(context.Background()); err != nil {
				t.Error(err)
				return
			}
			if err := st.Open(context.Background()); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
}

func TestAcceptanceTheStackCallsPricingThroughTheClient(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"amount_minor":1250}`)
	}))
	defer upstream.Close()

	s := newStack(t, 0, upstream.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	amount, err := s.pricing.Quote(context.Background(), "SKU-1")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if amount != 1250 {
		t.Errorf("amount = %d, want 1250", amount)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits = %d, want 1", got)
	}

	// Pin WHICH client the stack built. Replacing httpclient.New(...) with
	// &http.Client{} satisfies every assertion above -- while using
	// http.DefaultTransport, the one thing httpclient exists to avoid. That
	// mutant survived the whole suite at the R2 review.
	//
	// Since Task 5 wired resilience.Retry through Options.Middleware, the
	// transport httpclient.New returns is no longer the raw *http.Transport
	// -- httpclient's own TestMiddlewareWrapsTheTransport documents the same
	// shape -- so asserting the concrete type IS *http.Transport now catches
	// the &http.Client{} mutant (http.DefaultTransport is one) as well as a
	// buildStack that dropped Options.Middleware entirely.
	switch tr := s.pricing.http.Transport.(type) {
	case nil:
		t.Fatal("pricing client transport is nil; the stack must build it with httpclient.New, not a bare &http.Client{}")
	case *http.Transport:
		t.Fatal("pricing client transport is the raw *http.Transport; the stack must set httpclient.Options.Middleware so the pricing call retries")
	default:
		_ = tr
	}
}

// The client must be released at shutdown, and buildStack is where that wiring
// lives so the mutation gate covers it. Dropping the lc.Add must fail this
// test -- R1 shipped an OnServeError wiring that could be deleted with every
// test still green, which is why this asserts rather than trusts.
func TestAcceptanceShutdownReleasesThePricingClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"amount_minor":1}`)
	}))
	defer upstream.Close()

	s := newStack(t, 0, upstream.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()

	// Let startup finish, then shut down and wait for the unwind.
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := s.snapshot(); !slices.Contains(got, "stop:pricing-client") {
		t.Errorf("shutdown did not release the pricing client: ops = %v", got)
	}
}

// TestAcceptanceShutdownActuallyClosesThePricingConnection goes one step
// further than the trace check above: go-mutesting found that buildStack's
// call to pricing.CloseIdleConnections() can be replaced with a discarded
// method value -- compiling, never firing, and still leaving the
// "stop:pricing-client" trace line in place, since that line is written by
// the surrounding closure regardless of whether the call inside it runs.
//
// This proves the real effect the wiring exists for: a pooled, idle
// connection is actually torn down at shutdown, not merely announced. The
// pricing client's default httpclient.Options keep it alive for 90s
// (Task 2/3's IdleConnTimeout default), so nothing but an explicit
// CloseIdleConnections call could close it this quickly.
func TestAcceptanceShutdownActuallyClosesThePricingConnection(t *testing.T) {
	var mu sync.Mutex
	var closed bool
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"amount_minor":1}`)
	}))
	// ConnState must be set before Start, not after: the server begins
	// serving inside Start, and setting it on a live *http.Server races
	// with the server goroutine reading it on the next connection.
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			mu.Lock()
			closed = true
			mu.Unlock()
		}
	}
	upstream.Start()
	defer upstream.Close()

	s := newStack(t, 0, upstream.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Put a connection in the pool.
	if _, err := s.pricing.Quote(context.Background(), "SKU-1"); err != nil {
		t.Fatalf("Quote: %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got := closed
		mu.Unlock()
		if got || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if !closed {
		t.Error("shutdown did not close the pricing client's pooled connection")
	}
}

func TestAcceptanceThePricingCallRetriesATransientFailure(t *testing.T) {
	var attempts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "upstream is warming up")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"amount_minor":1250}`)
	}))
	defer upstream.Close()

	s := newStack(t, 0, upstream.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	amount, err := s.pricing.Quote(context.Background(), "SKU-1")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if amount != 1250 {
		t.Errorf("amount = %d, want 1250", amount)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("upstream saw %d attempts, want 3 (two failures then a success)", got)
	}
}

// TestAcceptanceThePricingCallGivesUpAfterMaxAttempts pins the policy's
// MaxAttempts, not just that retry happens at all. The test above still
// passes with MaxAttempts raised to 4 or more, since the upstream there
// recovers on the third try and nothing after that is ever attempted -- a
// mutation.sh run against the committed wiring found MaxAttempts: 3 -> 4
// surviving for exactly that reason. Here the upstream never recovers, so
// the exact number of attempts is the only thing that can distinguish the
// policy's ceiling from a higher one.
func TestAcceptanceThePricingCallGivesUpAfterMaxAttempts(t *testing.T) {
	var attempts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "upstream never recovers")
	}))
	defer upstream.Close()

	s := newStack(t, 0, upstream.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.lc.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	if _, err := s.pricing.Quote(context.Background(), "SKU-1"); err == nil {
		t.Fatal("Quote succeeded against an upstream that always returns 503")
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("upstream saw %d attempts, want exactly 3 (MaxAttempts)", got)
	}
}
