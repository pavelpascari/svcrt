package main

import (
	"context"

	"github.com/pavelpascari/svcrt/contract"
)

type submitRequest struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
}

type invalidNotificationError struct{ field string }

func (e invalidNotificationError) Error() string     { return "invalid notification field: " + e.field }
func (e invalidNotificationError) ErrorCode() string { return "notification.invalid" }
func (e invalidNotificationError) ErrorParams() map[string]any {
	return map[string]any{"field": e.field}
}

var _ contract.Detailed = invalidNotificationError{}

func validateNotification(next contract.Handler[submitRequest, Notification]) contract.Handler[submitRequest, Notification] {
	return func(ctx context.Context, req submitRequest) (Notification, error) {
		if req.Recipient == "" {
			return Notification{}, invalidNotificationError{field: "recipient"}
		}
		if req.Message == "" {
			return Notification{}, invalidNotificationError{field: "message"}
		}
		return next(ctx, req)
	}
}
