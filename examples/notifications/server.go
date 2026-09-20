package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/pavelpascari/svcrt/contract"
	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/telemetry"
)

func newHandler(store *notificationStore, log *slog.Logger) http.Handler {
	submit := contract.Chain(validateNotification)(func(ctx context.Context, req submitRequest) (Notification, error) {
		return store.submit(ctx, req.Recipient, req.Message), nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /notifications", func(w http.ResponseWriter, r *http.Request) {
		var in submitRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeContractError(w, r, log, invalidNotificationError{field: "body"})
			return
		}
		n, err := submit(r.Context(), in)
		if err != nil {
			writeContractError(w, r, log, err)
			return
		}
		writeJSON(w, http.StatusAccepted, struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}{ID: n.ID, Status: n.Status})
	})
	mux.HandleFunc("GET /notifications/{id}", func(w http.ResponseWriter, r *http.Request) {
		n, ok := store.get(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, n)
	})

	return httpserver.Chain(
		telemetry.Server(telemetry.Options{}),
		httpserver.AccessLog(log),
		httpserver.Recover(log),
	)(mux)
}

func writeContractError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var coded contract.Coded
	if !errors.As(err, &coded) {
		log.ErrorContext(r.Context(), "unhandled error", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{"code": "internal"},
		})
		return
	}

	var params map[string]any
	var detailed contract.Detailed
	if errors.As(err, &detailed) {
		params = detailed.ErrorParams()
	}
	log.InfoContext(r.Context(), "request rejected", contract.KeyCode, coded.ErrorCode())
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{"code": coded.ErrorCode(), "params": params},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
