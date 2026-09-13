package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// erroringRoundTripper always fails, standing in for a dead connection or a
// DNS failure -- the "call pricing" branch in Quote never gets exercised by
// the acceptance suite, which only calls Quote against a live upstream.
type erroringRoundTripper struct{}

func (erroringRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("boom")
}

// Every failure branch in Quote must report both a non-nil error AND a zero
// amount. Checking only err != nil is not enough: go-mutesting found three
// survivors per branch that changed the paired zero to a nonzero literal
// (0 -> 1, 0 -> -1) and one that dropped the return entirely -- a test that
// ignores the amount on the error path cannot distinguish any of them.
func TestQuoteReturnsAZeroAmountWhenRequestConstructionFails(t *testing.T) {
	t.Parallel()

	// A raw control character in the query fails url.Parse inside
	// http.NewRequestWithContext, which is the only way to reach this branch
	// without a fake http.RoundTripper.
	p := NewPricingClient("http://example.com", &http.Client{})

	amount, err := p.Quote(context.Background(), "\n")
	if err == nil {
		t.Fatal("Quote succeeded with a sku that cannot be a valid URL")
	}
	if amount != 0 {
		t.Errorf("amount = %d, want 0 on error", amount)
	}
}

func TestQuoteReturnsAZeroAmountWhenTheHTTPCallFails(t *testing.T) {
	t.Parallel()

	p := NewPricingClient("http://example.com", &http.Client{Transport: erroringRoundTripper{}})

	amount, err := p.Quote(context.Background(), "SKU-1")
	if err == nil {
		t.Fatal("Quote succeeded despite the transport failing")
	}
	if amount != 0 {
		t.Errorf("amount = %d, want 0 on error", amount)
	}
}

// The body is a well-formed, decodable success payload on purpose: an empty
// body would also fail at the decode step below, so a status of 500 and an
// empty body cannot tell "the status check rejected this" apart from "the
// status check was skipped and decoding happened to fail anyway" -- which is
// exactly the shape of the survivor go-mutesting found when the status
// check's return was dropped and control fell through to a body that
// decoded to a real amount.
func TestQuoteReturnsAZeroAmountOnANonOKStatus(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"amount_minor":999}`)
	}))
	defer upstream.Close()

	p := NewPricingClient(upstream.URL, &http.Client{})

	amount, err := p.Quote(context.Background(), "SKU-1")
	if err == nil {
		t.Fatal("Quote succeeded against a 500 response")
	}
	if amount != 0 {
		t.Errorf("amount = %d, want 0 on error", amount)
	}
}

func TestQuoteReturnsAZeroAmountOnMalformedJSON(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{not json`)
	}))
	defer upstream.Close()

	p := NewPricingClient(upstream.URL, &http.Client{})

	amount, err := p.Quote(context.Background(), "SKU-1")
	if err == nil {
		t.Fatal("Quote succeeded despite a malformed response body")
	}
	if amount != 0 {
		t.Errorf("amount = %d, want 0 on error", amount)
	}
}

// spyTransport records whether CloseIdleConnections was delegated to it.
// http.Client.CloseIdleConnections type-asserts its Transport against an
// interface with exactly this method and calls it when present -- which is
// what makes this a faithful spy rather than a fake that reimplements
// PricingClient's job.
type spyTransport struct{ closed bool }

func (*spyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}
func (s *spyTransport) CloseIdleConnections() { s.closed = true }

// PricingClient.CloseIdleConnections must actually delegate, not merely
// exist: go-mutesting found the delegating call itself replaceable with a
// discarded method value, which compiles, runs, and leaves every other test
// in this file green.
func TestCloseIdleConnectionsDelegatesToTheUnderlyingClient(t *testing.T) {
	t.Parallel()

	tr := &spyTransport{}
	p := NewPricingClient("http://example.com", &http.Client{Transport: tr})

	p.CloseIdleConnections()

	if !tr.closed {
		t.Error("CloseIdleConnections did not delegate to the underlying http.Client")
	}
}
