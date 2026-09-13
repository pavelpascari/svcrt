package resilience

import (
	"net/http"
	"strconv"
	"time"
)

// retryAfter reports the delay a Retry-After header asks for, and whether one
// was usable.
//
// It outranks the configured backoff when present: a server saying when to come
// back is better information than any local curve, and ignoring it is how a
// thundering herd forms. A malformed value falls back to the policy rather than
// failing the request -- a bad header is the server's bug, not the caller's.
//
// Honoured only on 429 and 503, the two statuses RFC 9110 defines it for.
//
// now is a parameter rather than a time.Now() call so the HTTP-date branch is
// testable without a clock seam.
func retryAfter(resp *http.Response, now time.Time) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
	default:
		return 0, false
	}

	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0, false
	}

	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}

	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}

	return 0, false
}
