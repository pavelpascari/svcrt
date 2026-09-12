package main

import "context"

// Store is the service's backing data. It exists so the exemplar has a real
// dependency edge: the API must not start before its data source.
type Store struct {
	orders map[string]*Order
	open   bool
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
	s.open = true
	return nil
}

// Close releases the store.
func (s *Store) Close(context.Context) error {
	s.open = false
	return nil
}

// Get returns an order. It reports not-found once the store is closed, so a
// request arriving after shutdown cannot read stale data.
func (s *Store) Get(id string) (*Order, bool) {
	if !s.open {
		return nil, false
	}
	o, ok := s.orders[id]
	return o, ok
}
