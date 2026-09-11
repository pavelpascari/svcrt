package main

import (
	"context"
	"errors"
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

func TestServiceReturnsOrder(t *testing.T) {
	t.Parallel()

	svc := NewService(map[string]*Order{"1": {ID: "1", Qty: 3}})

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

	svc := NewService(nil)

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
