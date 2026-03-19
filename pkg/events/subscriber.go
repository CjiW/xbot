package events

import (
	"context"
	"log"
	"time"
)

// Subscriber is the event subscriber interface.
type Subscriber interface {
	// OnEvent handles an event.
	// Returns nil for successful handling.
	// Returns error for failed handling (logs and isolates).
	OnEvent(ctx context.Context, event Event) error

	// Topics returns the list of subscribed topics.
	// Supports wildcard: "*" for all events.
	// Supports prefix: "agent.*" for all agent events.
	Topics() []string

	// Name returns the subscriber name (for logging and debugging).
	Name() string

	// IsEnabled returns whether the subscriber is enabled.
	IsEnabled() bool

	// Shutdown cleans up when the subscriber shuts down.
	Shutdown(ctx context.Context) error
}

// BaseSubscriber is a basic subscriber implementation.
type BaseSubscriber struct {
	name    string
	topics  []string
	enabled bool
}

// NewBaseSubscriber creates a base subscriber.
func NewBaseSubscriber(name string, topics []string) *BaseSubscriber {
	return &BaseSubscriber{
		name:    name,
		topics:  topics,
		enabled: true,
	}
}

// Topics implements the Subscriber interface.
func (s *BaseSubscriber) Topics() []string { return s.topics }

// Name implements the Subscriber interface.
func (s *BaseSubscriber) Name() string { return s.name }

// IsEnabled implements the Subscriber interface.
func (s *BaseSubscriber) IsEnabled() bool { return s.enabled }

// Shutdown implements the Subscriber interface.
func (s *BaseSubscriber) Shutdown(ctx context.Context) error {
	s.enabled = false
	return nil
}

// Middleware is a subscriber middleware (for decorator pattern).
type Middleware func(Subscriber) Subscriber

// LoggingMiddleware is the logging middleware.
func LoggingMiddleware(next Subscriber) Subscriber {
	return &loggingSubscriber{next: next}
}

type loggingSubscriber struct {
	next Subscriber
}

func (s *loggingSubscriber) OnEvent(ctx context.Context, event Event) error {
	log.Printf("[EventBus] Subscriber %s handling event %s", s.Name(), event.Type())
	err := s.next.OnEvent(ctx, event)
	if err != nil {
		log.Printf("[EventBus] Subscriber %s failed: %v", s.Name(), err)
	}
	return err
}

func (s *loggingSubscriber) Topics() []string                   { return s.next.Topics() }
func (s *loggingSubscriber) Name() string                       { return s.next.Name() }
func (s *loggingSubscriber) IsEnabled() bool                    { return s.next.IsEnabled() }
func (s *loggingSubscriber) Shutdown(ctx context.Context) error { return s.next.Shutdown(ctx) }

// RetryMiddleware is the retry middleware.
func RetryMiddleware(maxRetries int, backoff time.Duration) Middleware {
	return func(next Subscriber) Subscriber {
		return &retrySubscriber{
			next:       next,
			maxRetries: maxRetries,
			backoff:    backoff,
		}
	}
}

type retrySubscriber struct {
	next       Subscriber
	maxRetries int
	backoff    time.Duration
}

func (s *retrySubscriber) OnEvent(ctx context.Context, event Event) error {
	var lastErr error
	for i := 0; i < s.maxRetries; i++ {
		err := s.next.OnEvent(ctx, event)
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(s.backoff)
	}
	return lastErr
}

func (s *retrySubscriber) Topics() []string                   { return s.next.Topics() }
func (s *retrySubscriber) Name() string                       { return s.next.Name() }
func (s *retrySubscriber) IsEnabled() bool                    { return s.next.IsEnabled() }
func (s *retrySubscriber) Shutdown(ctx context.Context) error { return s.next.Shutdown(ctx) }
