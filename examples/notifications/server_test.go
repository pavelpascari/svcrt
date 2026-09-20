package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pavelpascari/svcrt/testkit"
)

func TestHTTPMalformedJSONReturnsContractError(t *testing.T) {
	log, _ := testkit.Logger(t)
	handler := newHandler(newNotificationStore(), log)
	req := httptest.NewRequest(http.MethodPost, "/notifications", strings.NewReader(`{"recipient":`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var envelope struct {
		Error struct {
			Code   string         `json:"code"`
			Params map[string]any `json:"params"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error.Code != "notification.invalid" || envelope.Error.Params["field"] != "body" {
		t.Fatalf("error = %+v, want notification.invalid with field=body", envelope.Error)
	}
}
