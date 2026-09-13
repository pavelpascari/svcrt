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
	svc := NewService(openStore(map[string]*Order{"1": {ID: "1", Qty: 3}}))
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
// reaches the client, on any status this handler can produce.
func TestAcceptanceErrorBodyCarriesNoProse(t *testing.T) {
	t.Parallel()

	h, _ := newTestServer(t)

	cases := []struct {
		name  string
		path  string
		prose []string
	}{
		{"404 order_not_found", "/orders/nope", []string{"not found", "Order", "missing"}},
		{"400 id_too_long", "/orders/aaaaaaaaaaaaaaaaaaaa", []string{"exceeds", "id length"}},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))

		body := rec.Body.String()
		for _, prose := range tc.prose {
			if strings.Contains(body, prose) {
				t.Errorf("%s: body %q contains display prose %q", tc.name, body, prose)
			}
		}
	}

	// No fixture reaches writeError's non-Coded path over HTTP -- every error
	// the service and its middleware chain can raise implements
	// contract.Coded. writeError is called directly, as
	// TestWriteErrorUncodedIsA500WithoutLeakingTheCause does, so the same
	// no-prose rule is proven for the 500 path too.
	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{Level: slog.LevelDebug})
	rec := httptest.NewRecorder()
	writeError(rec, log, opaqueError{})

	if body := rec.Body.String(); strings.Contains(body, "10.0.0.5") {
		t.Errorf("500 body %q leaks the underlying cause", body)
	}
}

// opaqueError is not contract.Coded, standing in for a raw dependency
// failure -- a database dial error, say -- that must never reach the wire.
type opaqueError struct{}

func (opaqueError) Error() string { return "dial tcp 10.0.0.5:5432: connection refused" }

// This is the branch that stops a raw database string, hostname, or stack
// detail from reaching a caller. Each of its four properties can regress
// independently, so each gets its own assertion: the status is 500, the
// body's code is the generic "internal", the body does not contain the
// original error's text, and the log output does contain the cause --
// silently swallowing it would be its own defect.
func TestWriteErrorUncodedIsA500WithoutLeakingTheCause(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{Level: slog.LevelDebug})
	rec := httptest.NewRecorder()

	writeError(rec, log, opaqueError{})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body, err)
	}
	if env.Error.Code != "internal" {
		t.Errorf("code = %q, want internal", env.Error.Code)
	}

	if body := rec.Body.String(); strings.Contains(body, "10.0.0.5") {
		t.Errorf("body %q leaks the cause (%q)", body, opaqueError{}.Error())
	}

	if logs := buf.String(); !strings.Contains(logs, "10.0.0.5") {
		t.Errorf("log output %q does not contain the cause; a swallowed cause is its own defect", logs)
	}
}

// statusFor's default arm is reachable only by a Coded error whose code is
// missing from the switch. That is distinct from the non-Coded path above:
// writeError's non-Coded branch returns 500 unconditionally and never calls
// statusFor at all.
func TestStatusForDefaultsToInternalServerErrorForUnregisteredCode(t *testing.T) {
	t.Parallel()

	if got := statusFor("unregistered_code"); got != http.StatusInternalServerError {
		t.Errorf("statusFor(unregistered_code) = %d, want %d", got, http.StatusInternalServerError)
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

	svc := NewService(openStore(map[string]*Order{"1": {ID: "1", Qty: 3}}))
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

// Every response this handler produces is JSON, and a client that has to
// sniff the body to find that out is a client we broke. Deleting the header
// used to pass the whole suite, on both the success and the error path.
func TestResponsesDeclareJSONContentType(t *testing.T) {
	t.Parallel()

	h, _ := newTestServer(t)

	cases := []struct {
		name, path string
		wantStatus int
	}{
		{"success", "/orders/1", http.StatusOK},
		{"error", "/orders/nope", http.StatusNotFound},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))

		if rec.Code != tc.wantStatus {
			t.Fatalf("%s: status = %d, want %d", tc.name, rec.Code, tc.wantStatus)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("%s: Content-Type = %q, want application/json", tc.name, got)
		}
	}
}

// RejectLongID(8) caps the id at 8, so 8 is accepted and 9 is not. Only the
// accepted-at-exactly-max case distinguishes `>` from `>=`, and nothing
// exercised it: every other test used an id far past the limit.
func TestIDLengthBoundaryAtExactlyMax(t *testing.T) {
	t.Parallel()

	h, _ := newTestServer(t)

	cases := []struct {
		name, id   string
		wantStatus int
		wantCode   string
	}{
		{"exactly max is accepted", "12345678", http.StatusNotFound, "order_not_found"},
		{"one over max is rejected", "123456789", http.StatusBadRequest, "id_too_long"},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/orders/"+tc.id, nil))

		if rec.Code != tc.wantStatus {
			t.Errorf("%s (id %q, len %d): status = %d, want %d",
				tc.name, tc.id, len(tc.id), rec.Code, tc.wantStatus)
		}

		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: decode body %q: %v", tc.name, rec.Body, err)
		}
		if env.Error.Code != tc.wantCode {
			t.Errorf("%s: code = %q, want %q", tc.name, env.Error.Code, tc.wantCode)
		}
	}
}

// contract.KeyCode had no producer anywhere in the repo. A well-known key
// nobody writes is a convention nobody follows, so writeError emits it on
// both of its branches -- which is also what lets an operator join a
// server-side line to the envelope a client received.
func TestWriteErrorLogsTheErrorCodeUnderTheWellKnownKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"coded", notFoundError{id: "nope"}, "order_not_found"},
		{"uncoded", opaqueError{}, "internal"},
	}

	for _, tc := range cases {
		var buf bytes.Buffer
		log := logging.New(&buf, logging.Options{Level: slog.LevelDebug})
		writeError(httptest.NewRecorder(), log, tc.err)

		line := map[string]any{}
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
			t.Fatalf("%s: decode log %q: %v", tc.name, buf.String(), err)
		}
		if line[contract.KeyCode] != tc.wantCode {
			t.Errorf("%s: %s = %v, want %q", tc.name, contract.KeyCode, line[contract.KeyCode], tc.wantCode)
		}
	}
}
