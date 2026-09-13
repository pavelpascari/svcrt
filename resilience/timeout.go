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
