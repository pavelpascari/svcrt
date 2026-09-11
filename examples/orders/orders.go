package main

import (
	"context"
	"fmt"

	"github.com/pavelpascari/svcrt/contract"
)

// Order is the response type. Only the code and scalar data cross the wire;
// nothing here is a display string.
type Order struct {
	ID  string `json:"id"`
	Qty int    `json:"qty"`
}

type GetOrderRequest struct {
	ID string
}

// notFoundError has nothing to substitute, so it implements Coded alone.
type notFoundError struct{ id string }

func (e notFoundError) Error() string     { return fmt.Sprintf("order %s not found", e.id) }
func (e notFoundError) ErrorCode() string { return "order_not_found" }

// idTooLongError carries the values a client needs to render a message in any
// language, so it opts into Detailed. Note the params are scalars: a client
// substitutes them into a template it owns, and no sentence is ever written
// here.
type idTooLongError struct{ Max, Got int }

func (e idTooLongError) Error() string {
	return fmt.Sprintf("id length %d exceeds max %d", e.Got, e.Max)
}
func (e idTooLongError) ErrorCode() string { return "id_too_long" }
func (e idTooLongError) ErrorParams() map[string]any {
	return map[string]any{"max": e.Max, "got": e.Got}
}

// Declared explicitly so a typo in a method name fails the build rather than
// silently dropping the interface.
var (
	_ contract.Coded    = notFoundError{}
	_ contract.Detailed = idTooLongError{}
)

// Service is the hand-written business logic. Its method shape is the one
// svcgen will generate a handler for: ctx first, one request struct, a
// pointer response and an error.
type Service struct {
	orders map[string]*Order
}

func NewService(orders map[string]*Order) *Service {
	if orders == nil {
		orders = map[string]*Order{}
	}
	return &Service{orders: orders}
}

func (s *Service) GetOrder(ctx context.Context, req GetOrderRequest) (*Order, error) {
	o, ok := s.orders[req.ID]
	if !ok {
		return nil, notFoundError{id: req.ID}
	}
	return o, nil
}
