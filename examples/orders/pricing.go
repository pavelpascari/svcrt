package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// PricingClient calls the pricing service. It is the only thing here that
// knows that service's wire shape.
type PricingClient struct {
	baseURL string
	http    *http.Client
}

// NewPricingClient returns a client calling baseURL through c.
func NewPricingClient(baseURL string, c *http.Client) *PricingClient {
	return &PricingClient{baseURL: baseURL, http: c}
}

// Quote returns the price of sku in minor units.
func (p *PricingClient) Quote(ctx context.Context, sku string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/quote?sku="+sku, nil)
	if err != nil {
		return 0, fmt.Errorf("build pricing request: %w", err)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("call pricing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("pricing returned %d", resp.StatusCode)
	}

	var body struct {
		AmountMinor int64 `json:"amount_minor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode pricing response: %w", err)
	}
	return body.AmountMinor, nil
}

// CloseIdleConnections releases pooled connections. Wired into the lifecycle by
// buildStack.
func (p *PricingClient) CloseIdleConnections() { p.http.CloseIdleConnections() }
