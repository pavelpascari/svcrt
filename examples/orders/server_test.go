package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pavelpascari/svcrt/contract"
	"github.com/pavelpascari/svcrt/logging"
)

func newTestServer(t *testing.T) (http.Handler, func() string) {
	t.Helper()
	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{Level: slog.LevelDebug})
	svc := NewService(map[string]*Order{"1": {ID: "1", Qty: 3}})
	return newServer(svc, log), buf.String
}

// --- the R0 acceptance test: config + logging + contract composed ---

func TestAcceptanceKnownOrderReturns200(t *testing.T) {
	t.Parallel()

	h, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/orders/1", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}

	var got Order
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body, err)
	}
	if got.ID != "1" || got.Qty != 3 {
		t.Errorf("order = %+v, want {ID:1 Qty:3}", got)
	}
}

func TestAcceptanceUnknownOrderReturns404WithCodeEnvelope(t *testing.T) {
	t.Parallel()

	h, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/orders/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	var env struct {
		Error struct {
			Code   string         `json:"code"`
			Params map[string]any `json:"params"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body, err)
	}
	if env.Error.Code != "order_not_found" {
		t.Errorf("code = %q, want order_not_found", env.Error.Code)
	}
	if env.Error.Params != nil {
		t.Errorf("params = %v, want absent for a paramless error", env.Error.Params)
	}
}

// The whole point of the codes-not-prose rule: no server-authored sentence
// reaches the client.
func TestAcceptanceErrorBodyCarriesNoProse(t *testing.T) {
	t.Parallel()

	h, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/orders/nope", nil))

	body := rec.Body.String()
	for _, prose := range []string{"not found", "Order", "missing"} {
		if strings.Contains(body, prose) {
			t.Errorf("body %q contains display prose %q", body, prose)
		}
	}
}

func TestAcceptanceLogLineCarriesRouteAndStatus(t *testing.T) {
	t.Parallel()

	h, logs := newTestServer(t)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/orders/1", nil))

	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logs())), &line); err != nil {
		t.Fatalf("decode log %q: %v", logs(), err)
	}
	if line[logging.KeyRoute] != "GET /orders/{id}" {
		t.Errorf("%s = %v, want the route pattern", logging.KeyRoute, line[logging.KeyRoute])
	}
	if line[logging.KeyStatus] != float64(200) {
		t.Errorf("%s = %v, want 200", logging.KeyStatus, line[logging.KeyStatus])
	}
}

// --- pressure on contract's generic seam ---

func TestTypedMiddlewareChainAppliesInOrder(t *testing.T) {
	t.Parallel()

	svc := NewService(map[string]*Order{"1": {ID: "1", Qty: 3}})
	var calls int

	h := contract.Chain(RejectEmptyID, CountCalls(&calls))(svc.GetOrder)

	if _, err := h(context.Background(), GetOrderRequest{ID: "1"}); err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}

	// RejectEmptyID is outermost, so it short-circuits before CountCalls runs.
	if _, err := h(context.Background(), GetOrderRequest{ID: ""}); err == nil {
		t.Fatal("empty ID was accepted")
	}
	if calls != 1 {
		t.Errorf("calls = %d after a rejected request, want 1", calls)
	}
}

// This is the end-to-end proof that params reach the wire: a Detailed error
// raised inside a typed middleware, serialized by the hand-written envelope,
// and carrying the scalars a client needs to render any language.
func TestAcceptanceLongIDReturns400WithParams(t *testing.T) {
	t.Parallel()

	h, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/orders/aaaaaaaaaaaaaaaaaaaa", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body)
	}

	var env struct {
		Error struct {
			Code   string         `json:"code"`
			Params map[string]any `json:"params"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body, err)
	}
	if env.Error.Code != "id_too_long" {
		t.Errorf("code = %q, want id_too_long", env.Error.Code)
	}
	if env.Error.Params["max"] != float64(8) {
		t.Errorf("params[max] = %v, want 8", env.Error.Params["max"])
	}
	if env.Error.Params["got"] != float64(20) {
		t.Errorf("params[got] = %v, want 20", env.Error.Params["got"])
	}
}

// A {id} wildcard never matches an empty path segment, so an empty id cannot
// arrive over HTTP -- the mux 404s first. RejectEmptyID is therefore covered
// only by the direct test above, and this documents why there is no HTTP-level
// test for it.
func TestEmptyIDNeverReachesTheHandlerOverHTTP(t *testing.T) {
	t.Parallel()

	h, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/orders/", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 from the mux", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "order_not_found") {
		t.Errorf("body %q came from our handler; the mux should have rejected the route", body)
	}
}
