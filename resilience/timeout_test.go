package resilience_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/resilience"
)

func TestTimeoutBoundsTheAttempt(t *testing.T) {
	t.Parallel()
	rt := resilience.Timeout(50 * time.Millisecond)(rtFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))

	_, err := rt.RoundTrip(get(t))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}

// The timeout must not cancel the context before the caller has read the body.
// Cancelling on return would make every successful response unreadable.
func TestTimeoutLeavesTheBodyReadableAfterReturn(t *testing.T) {
	t.Parallel()
	rt := resilience.Timeout(5 * time.Second)(rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("payload")),
		}, nil
	}))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body after RoundTrip returned: %v", err)
	}
	if string(b) != "payload" {
		t.Errorf("body = %q, want %q", b, "payload")
	}
}

// When the wrapped RoundTripper errors, Timeout must still release the
// timeout context's resources on that path -- it cannot rely on the body's
// Close, since there is no body. This also directly demonstrates the bug
// TestTimeoutLeavesTheBodyReadableAfterReturn's `defer cancel()` break-it
// experiment could not: capturing the request's context and checking it
// synchronously after RoundTrip returns proves cancel() ran, without
// depending on a fake transport that happens to be sensitive to
// cancellation.
func TestTimeoutCancelsTheContextWhenRoundTripErrors(t *testing.T) {
	t.Parallel()
	var captured context.Context
	boom := errors.New("boom")
	rt := resilience.Timeout(5 * time.Second)(rtFunc(func(r *http.Request) (*http.Response, error) {
		captured = r.Context()
		return nil, boom
	}))

	_, err := rt.RoundTrip(get(t))
	if !errors.Is(err, boom) {
		t.Fatalf("RoundTrip err = %v, want %v", err, boom)
	}
	if !errors.Is(captured.Err(), context.Canceled) {
		t.Errorf("context.Err() after an error return = %v, want context.Canceled -- the error path must still release cancel", captured.Err())
	}
}

// Closing the response body must release the timeout context promptly, not
// only "eventually" when the timeout itself elapses -- otherwise a caller
// that closes bodies correctly still leaks a live timer until d expires.
func TestTimeoutClosingTheBodyCancelsTheContext(t *testing.T) {
	t.Parallel()
	var captured context.Context
	rt := resilience.Timeout(5 * time.Second)(rtFunc(func(r *http.Request) (*http.Response, error) {
		captured = r.Context()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("x")),
		}, nil
	}))

	resp, err := rt.RoundTrip(get(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if err := captured.Err(); err != nil {
		t.Fatalf("context already done before Close: %v", err)
	}

	resp.Body.Close()

	if !errors.Is(captured.Err(), context.Canceled) {
		t.Errorf("context.Err() after Close = %v, want context.Canceled -- Close must release the timeout's cancel", captured.Err())
	}
}
