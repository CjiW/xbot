package logger

import (
	"context"
	"strings"

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

// NewRequestID generates a request ID (UUID without dashes).
func NewRequestID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

// Ctx returns a logrus Entry with the request_id field from context (if present).
// Use this as the starting point for structured logging within a request scope.
func Ctx(ctx context.Context) *Entry {
	if id := RequestID(ctx); id != "" {
		return WithField("request_id", id)
	}
	return WithFields(Fields{})
}
