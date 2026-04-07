package event

import "time"

// Event represents an incoming event from an external source.
// Events are transient and not persisted.
type Event struct {
	Type      string            // "webhook", "github", "gitlab", etc.
	Source    string            // source identifier, e.g. trigger ID
	Payload   map[string]any    // parsed JSON payload
	Headers   map[string]string // HTTP headers (for webhook events)
	RawBody   []byte            // raw request body (for signature verification)
	Timestamp time.Time
}
