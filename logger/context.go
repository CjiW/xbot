package logger

import (
	"context"

	"github.com/google/uuid"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

// WithRequestID injects a request ID into the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID extracts the request ID from the context. Returns "" if not set.
func RequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// NewRequestID generates a short request ID (first 8 chars of UUID).
func NewRequestID() string {
	return uuid.New().String()[:8]
}

// Ctx returns a logrus Entry with the request_id field from context (if present).
// Use this as the starting point for structured logging within a request scope.
func Ctx(ctx context.Context) *Entry {
	if id := RequestID(ctx); id != "" {
		return WithField("req", id)
	}
	return WithFields(Fields{})
}
