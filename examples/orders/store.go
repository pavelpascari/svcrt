package main

import (
	"context"
	"sync/atomic"
)

// Store is the service's backing data. It exists so the exemplar has a real
// dependency edge: the API must not start before its data source.
type Store struct {
	orders map[string]*Order

	// open is atomic because Get runs on request goroutines while Close runs
	// on the lifecycle's stop goroutine. The dependency edge (api is
	// After(store), so api.Shutdown drains before store.Close) usually keeps
	// them apart -- but only usually: a Shutdown that returns on its deadline
	// instead, because a handler outlived StopTimeout or hijacked its
	// connection, leaves Close writing this flag under a still-running Get.
	// Get's whole promise is about that window, so it has to hold in it.
	open atomic.Bool
}

func NewStore(orders map[string]*Order) *Store {
	if orders == nil {
		orders = map[string]*Order{}
	}
	return &Store{orders: orders}
}

// Open readies the store. A real one would dial a database; this one flips a
// flag, which is enough to make the dependency edge meaningful.
func (s *Store) Open(context.Context) error {
	s.open.Store(true)
	return nil
}

// Close releases the store.
func (s *Store) Close(context.Context) error {
	s.open.Store(false)
	return nil
}

// Get returns an order. It reports not-found once the store is closed, so a
// request arriving after shutdown cannot read stale data.
func (s *Store) Get(id string) (*Order, bool) {
	if !s.open.Load() {
		return nil, false
	}
	o, ok := s.orders[id]
	return o, ok
}
