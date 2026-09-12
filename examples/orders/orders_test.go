package main

import (
	"context"
	"errors"
	"strings"
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
