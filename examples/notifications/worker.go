package main

import (
	"context"
	"log/slog"
	"sync"
)

type deliveryWorker struct {
	store    *notificationStore
	provider *providerClient
	log      *slog.Logger

	stopOnce sync.Once
	done     chan struct{}
}

func newDeliveryWorker(store *notificationStore, provider *providerClient, log *slog.Logger) *deliveryWorker {
	return &deliveryWorker{store: store, provider: provider, log: log, done: make(chan struct{})}
}

func (w *deliveryWorker) start(context.Context) error {
	go func() {
		defer close(w.done)
		for item := range w.store.queue {
			err := w.provider.deliver(item.ctx, item.notification)
			if err != nil {
				w.store.setStatus(item.notification.ID, "failed")
				w.log.ErrorContext(item.ctx, "notification delivery failed",
					"notification_id", item.notification.ID, "err", err)
				continue
			}
			w.store.setStatus(item.notification.ID, "delivered")
			w.log.InfoContext(item.ctx, "notification delivered",
				"notification_id", item.notification.ID)
		}
	}()
	return nil
}

func (w *deliveryWorker) stop(ctx context.Context) error {
	w.stopOnce.Do(func() { close(w.store.queue) })
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
