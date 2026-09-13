package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/pavelpascari/svcrt/contract"
	"github.com/pavelpascari/svcrt/httpserver"
)

// statusFor maps an error code to an HTTP status.
//
// EVERYTHING BELOW THIS COMMENT IS TEMPORARY. This table, the decode of the
// path parameter, the envelope construction, and writeError are exactly what
// svcgen emits at G0/G1 from a svcgen:status directive on each code. It is
// hand-written here so the runtime APIs have a real caller before the
// generator exists -- and so the boilerplate G1 deletes is visible and
// annoying rather than hypothetical.
func statusFor(code string) int {
	switch code {
	case "order_not_found":
		return http.StatusNotFound
	case "id_too_long":
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// errorEnvelope is the wire shape: a code, and the scalar params a client
// needs to render a message itself. No server-authored prose, ever.
type errorEnvelope struct {
	Error struct {
		Code   string         `json:"code"`
		Params map[string]any `json:"params,omitempty"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, log *slog.Logger, err error) {
	var coded contract.Coded
	if !errors.As(err, &coded) {
		// An error that is not part of the API surface is a 500. The cause is
		// logged and never serialized.
		log.Error("unhandled error", contract.KeyCode, "internal", "err", err)
		writeJSON(w, http.StatusInternalServerError, envelopeFor("internal", nil))
		return
	}

	var params map[string]any
	var detailed contract.Detailed
	if errors.As(err, &detailed) {
		params = detailed.ErrorParams()
	}

	// contract.KeyCode is a well-known key precisely so a server-side line and
	// the envelope the client received can be joined on the same value. Both
	// branches here emit it, so every error response has a matching line.
	// The params are deliberately not logged: they are already implied by the
	// code, and logging them is how a Secret eventually ends up in a log.
	code := coded.ErrorCode()
	log.Info("request failed", contract.KeyCode, code)

	writeJSON(w, statusFor(code), envelopeFor(code, params))
}

func envelopeFor(code string, params map[string]any) errorEnvelope {
	var e errorEnvelope
	e.Error.Code = code
	e.Error.Params = params
	return e
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// newServer wires the service into an http.Handler.
//
// It returns a handler, not a server: nothing here binds a port, installs a
// signal handler, or registers a health endpoint. main owns all of that.
func newServer(svc *Service, log *slog.Logger) http.Handler {
	getOrder := contract.Chain(RejectEmptyID, RejectLongID(8))(svc.GetOrder)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		req := GetOrderRequest{ID: r.PathValue("id")}

		order, err := getOrder(r.Context(), req)
		if err != nil {
			writeError(w, log, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
	})

	return httpserver.AccessLog(log)(mux)
}
