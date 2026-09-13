package resilience

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Timeout bounds a single attempt.
//
// Compose it with Retry to say which you mean:
//
//	Chain(Retry(p), Timeout(d))  // d bounds EACH attempt
//	Chain(Timeout(d), Retry(p))  // d bounds the whole retry sequence
//
// That is why this is middleware rather than a Policy field: a field could
// express only one of the two.
func Timeout(d time.Duration) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			ctx, cancel := context.WithTimeout(req.Context(), d)

			resp, err := next.RoundTrip(req.WithContext(ctx))
			if err != nil {
				cancel()
				return nil, err
			}

			// A RoundTripper may return a non-nil response with a nil Body.
			// http.RoundTripper's contract does not forbid it and drain
			// already guards for it (retry.go), so it is a case this module
			// treats as reachable. Wrapping it unconditionally would turn a
			// nil Body into a NON-nil io.ReadCloser holding a nil interface:
			// Close panics, and every downstream `resp.Body == nil` check --
			// including drain's -- goes blind, so Retry(Timeout(...)) panics
			// inside the one place that defends against this.
			//
			// http.NoBody is non-nil, always EOF, always a nil error, so
			// drain reads zero bytes and closes cleanly, and cancel still
			// fires on Close rather than leaking until d elapses.
			if resp.Body == nil {
				resp.Body = http.NoBody
			}

			// Deliberately NOT `defer cancel()`. The response body is read
			// after this function returns, and cancelling the context closes
			// it -- every successful response would come back unreadable.
			// Ownership of cancel passes to the body.
			resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
			return resp, nil
		})
	}
}

// cancelOnClose releases a context when the response body is closed, so the
// attempt's deadline covers the body read without outliving it.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
