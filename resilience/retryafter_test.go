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
		{"delta of exactly -1 is still ignored", http.StatusServiceUnavailable, "-1", 0, false},
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
	d, ok := retryAfter(nil, time.Now())
	if ok {
		t.Error("retryAfter(nil) reported a delay")
	}
	if d != 0 {
		t.Errorf("retryAfter(nil) duration = %v, want 0", d)
	}
}

// The HTTP-date branch clamps a past date to zero, not to any negative
// value. http.ParseTime has one-second resolution, so an ordinary "now" and
// header pair can only ever differ by a whole number of seconds -- landing
// exactly on the boundary (a negative delta of exactly one nanosecond, the
// smallest possible negative Duration) needs now to carry sub-second
// precision that the header's whole-second timestamp does not.
func TestRetryAfterHTTPDateClampsAOneNanosecondPastDateToZero(t *testing.T) {
	t.Parallel()
	// A header parsed to exactly the second boundary; now is one nanosecond
	// past it, so the parsed time is one nanosecond in the past.
	now := time.Date(2026, 9, 13, 12, 0, 0, 1, time.UTC)
	resp := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header)}
	resp.Header.Set("Retry-After", "Sun, 13 Sep 2026 12:00:00 GMT")

	got, ok := retryAfter(resp, now)
	if !ok {
		t.Fatal("retryAfter did not report a delay")
	}
	if got != 0 {
		t.Errorf("retryAfter one nanosecond in the past = %v, want 0 (clamped)", got)
	}
}
