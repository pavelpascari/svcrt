package contract_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/pavelpascari/svcrt/contract"
)

type req struct{ ID string }
type res struct{ Name string }

// trace returns a middleware that records entry and exit around next.
func trace(log *[]string, name string) contract.Middleware[req, *res] {
	return func(next contract.Handler[req, *res]) contract.Handler[req, *res] {
		return func(ctx context.Context, r req) (*res, error) {
			*log = append(*log, name+":before")
			out, err := next(ctx, r)
			*log = append(*log, name+":after")
			return out, err
		}
	}
}

func TestChainAppliesFirstArgumentOutermost(t *testing.T) {
	var log []string
	base := func(ctx context.Context, r req) (*res, error) {
		log = append(log, "handler")
		return &res{Name: r.ID}, nil
	}

	h := contract.Chain(trace(&log, "a"), trace(&log, "b"))(base)

	out, err := h(context.Background(), req{ID: "x"})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if out.Name != "x" {
		t.Errorf("Name = %q, want %q", out.Name, "x")
	}

	want := []string{"a:before", "b:before", "handler", "b:after", "a:after"}
	if !slices.Equal(log, want) {
		t.Errorf("call order = %v, want %v", log, want)
	}
}

func TestChainWithNoMiddlewareIsIdentity(t *testing.T) {
	sentinel := errors.New("sentinel")
	base := func(ctx context.Context, r req) (*res, error) { return nil, sentinel }

	h := contract.Chain[req, *res]()(base)

	if _, err := h(context.Background(), req{}); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
}

func TestMiddlewareCanShortCircuit(t *testing.T) {
	denied := errors.New("denied")
	reject := func(next contract.Handler[req, *res]) contract.Handler[req, *res] {
		return func(ctx context.Context, r req) (*res, error) { return nil, denied }
	}
	called := false
	base := func(ctx context.Context, r req) (*res, error) {
		called = true
		return &res{}, nil
	}

	h := contract.Chain(reject)(base)

	if _, err := h(context.Background(), req{}); !errors.Is(err, denied) {
		t.Errorf("err = %v, want %v", err, denied)
	}
	if called {
		t.Error("base handler ran despite short-circuit")
	}
}
