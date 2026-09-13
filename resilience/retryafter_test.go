package resilience

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryAfterParsing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name   string
		code   int
		header string
		want   time.Duration
		wantOK bool
	}{
		{"delta seconds on 429", http.StatusTooManyRequests, "120", 120 * time.Second, true},
		{"delta seconds on 503", http.StatusServiceUnavailable, "5", 5 * time.Second, true},
		{"http date in the future", http.StatusServiceUnavailable, "Sun, 13 Sep 2026 12:00:30 GMT", 30 * time.Second, true},
		{"http date in the past clamps to zero", http.StatusServiceUnavailable, "Sun, 13 Sep 2026 11:59:00 GMT", 0, true},
		{"absent header", http.StatusServiceUnavailable, "", 0, false},
		{"malformed header falls back", http.StatusServiceUnavailable, "soon please", 0, false},
		{"negative delta is ignored", http.StatusServiceUnavailable, "-5", 0, false},
		{"ignored on 502", http.StatusBadGateway, "120", 0, false},
		{"ignored on 500", http.StatusInternalServerError, "120", 0, false},
	} {
		resp := &http.Response{StatusCode: tc.code, Header: make(http.Header)}
		if tc.header != "" {
			resp.Header.Set("Retry-After", tc.header)
		}
		got, ok := retryAfter(resp, now)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("%s: retryAfter = (%v, %v), want (%v, %v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestRetryAfterOfANilResponse(t *testing.T) {
	t.Parallel()
	if _, ok := retryAfter(nil, time.Now()); ok {
		t.Error("retryAfter(nil) reported a delay")
	}
}
