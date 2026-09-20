package main

import (
	"context"
	"fmt"
	"sync"
)

type Notification struct {
	ID        string `json:"id"`
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
	Status    string `json:"status"`
}

type queuedNotification struct {
	ctx          context.Context
	notification Notification
}

type notificationStore struct {
	mu     sync.RWMutex
	nextID int
	items  map[string]Notification
	queue  chan queuedNotification
}

func newNotificationStore() *notificationStore {
	return &notificationStore{
		items: make(map[string]Notification),
		queue: make(chan queuedNotification, 64),
	}
}

func (s *notificationStore) submit(ctx context.Context, recipient, message string) Notification {
	s.mu.Lock()
	s.nextID++
	n := Notification{
		ID:        fmt.Sprintf("notification-%d", s.nextID),
		Recipient: recipient,
		Message:   message,
		Status:    "queued",
	}
	s.items[n.ID] = n
	s.mu.Unlock()

	s.queue <- queuedNotification{ctx: context.WithoutCancel(ctx), notification: n}
	return n
}

func (s *notificationStore) get(id string) (Notification, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.items[id]
	return n, ok
}

func (s *notificationStore) setStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.items[id]
	n.Status = status
	s.items[id] = n
}
