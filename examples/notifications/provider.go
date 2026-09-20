package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type providerClient struct {
	baseURL string
	http    *http.Client
}

func (p *providerClient) deliver(ctx context.Context, n Notification) error {
	body, err := json.Marshal(struct {
		Recipient string `json:"recipient"`
		Message   string `json:"message"`
	}{Recipient: n.Recipient, Message: n.Message})
	if err != nil {
		return fmt.Errorf("encode provider request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		p.baseURL+"/deliveries/"+url.PathEscape(n.ID), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build provider request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("call provider: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("provider returned %d", resp.StatusCode)
	}
	return nil
}

func (p *providerClient) closeIdleConnections() { p.http.CloseIdleConnections() }
