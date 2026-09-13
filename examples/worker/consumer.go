package main

import (
	"context"
	"sync"
	"time"
)

// Consumer stands in for a queue consumer: a component whose Start returns once
// it is running, with the actual work in a goroutine it owns.
type Consumer struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

func NewConsumer() *Consumer { return &Consumer{} }

// Start begins consuming and returns once the loop is running.
func (c *Consumer) Start(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})
	c.running = true

	go func() {
		defer close(c.done)
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// a real consumer would handle a message here
			}
		}
	}()
	return nil
}

// Stop ends the loop and waits for it, bounded by ctx.
func (c *Consumer) Stop(ctx context.Context) error {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	c.running = false
	cancel, done := c.cancel, c.done
	c.mu.Unlock()

	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Running reports whether the loop is active.
func (c *Consumer) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}
