// Package events provides a flexible event subscription system for xbot.
// It enables internal monitoring and external integration through event publishing and subscription.
package events

import (
	"fmt"
	"time"
)

// Event represents a unified event interface.
type Event interface {
	// Type returns the event type.
	Type() string
	// Timestamp returns the event timestamp.
	Timestamp() time.Time
	// Payload returns the event payload.
	Payload() interface{}
	// Source returns the event source (optional).
	Source() string
	// ID returns the unique event ID (optional).
	ID() string
}

// BaseEvent is a basic event implementation.
type BaseEvent struct {
	TypeValue    string      `json:"type"`
	TimeValue    time.Time   `json:"timestamp"`
	PayloadValue interface{} `json:"payload"`
	SourceValue  string      `json:"source,omitempty"`
	IDValue      string      `json:"id,omitempty"`
}

// Type implements the Event interface.
func (e *BaseEvent) Type() string { return e.TypeValue }

// Timestamp implements the Event interface.
func (e *BaseEvent) Timestamp() time.Time { return e.TimeValue }

// Payload implements the Event interface.
func (e *BaseEvent) Payload() interface{} { return e.PayloadValue }

// Source implements the Event interface.
func (e *BaseEvent) Source() string { return e.SourceValue }

// ID implements the Event interface.
func (e *BaseEvent) ID() string { return e.IDValue }

// NewEvent creates a new event.
func NewEvent(eventType string, payload interface{}) Event {
	return &BaseEvent{
		TypeValue:    eventType,
		TimeValue:    time.Now(),
		PayloadValue: payload,
		IDValue:      generateEventID(),
		SourceValue:  "xbot",
	}
}

// generateEventID generates a unique event ID.
func generateEventID() string {
	return fmt.Sprintf("evt_%x", time.Now().UnixNano())
}
