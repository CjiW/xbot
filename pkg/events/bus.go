package events

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	ErrSubscriberNotFound = errors.New("subscriber not found")
	ErrEventBusShutdown   = errors.New("event bus is shutdown")
)

// EventBus is the event bus interface.
type EventBus interface {
	// Subscribe subscribes to events.
	Subscribe(subscriber Subscriber) error

	// Unsubscribe unsubscribes from events.
	Unsubscribe(subscriberName string) error

	// Publish publishes an event.
	Publish(ctx context.Context, event Event) error

	// PublishAsync publishes an event asynchronously (does not wait for handling to complete).
	PublishAsync(ctx context.Context, event Event) <-chan error

	// Shutdown shuts down the event bus.
	Shutdown(ctx context.Context) error

	// Stats returns statistics.
	Stats() Stats
}

// Stats represents statistics.
type Stats struct {
	TotalSubscribers int
	TotalEvents      uint64
	TotalErrors      uint64
	ByType           map[string]uint64
}

// eventBus is the event bus implementation.
type eventBus struct {
	mu           sync.RWMutex
	subscribers  map[string]Subscriber // name -> subscriber
	topicIndex   map[string][]string   // topic -> subscriber names
	shutdownFlag atomic.Bool
	stats        Stats
	wg           sync.WaitGroup
}

// NewEventBus creates an event bus.
func NewEventBus() EventBus {
	return &eventBus{
		subscribers: make(map[string]Subscriber),
		topicIndex:  make(map[string][]string),
		stats: Stats{
			ByType: make(map[string]uint64),
		},
	}
}

// Subscribe implements the EventBus interface.
func (b *eventBus) Subscribe(subscriber Subscriber) error {
	if b.shutdownFlag.Load() {
		return ErrEventBusShutdown
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	name := subscriber.Name()
	if _, exists := b.subscribers[name]; exists {
		log.Printf("[EventBus] Subscriber %s already exists, replacing", name)
	}

	b.subscribers[name] = subscriber

	// Update topic index
	for _, topic := range subscriber.Topics() {
		b.topicIndex[topic] = append(b.topicIndex[topic], name)
	}

	log.Printf("[EventBus] Subscriber %s subscribed to topics: %v", name, subscriber.Topics())
	return nil
}

// Unsubscribe implements the EventBus interface.
func (b *eventBus) Unsubscribe(subscriberName string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	subscriber, exists := b.subscribers[subscriberName]
	if !exists {
		return ErrSubscriberNotFound
	}

	// Remove subscriber
	delete(b.subscribers, subscriberName)

	// Remove from topic index
	for _, topic := range subscriber.Topics() {
		names := b.topicIndex[topic]
		var newNames []string
		for _, name := range names {
			if name != subscriberName {
				newNames = append(newNames, name)
			}
		}
		b.topicIndex[topic] = newNames
	}

	// Shutdown subscriber
	if err := subscriber.Shutdown(context.Background()); err != nil {
		log.Printf("[EventBus] Failed to shutdown subscriber %s: %v", subscriberName, err)
	}

	log.Printf("[EventBus] Subscriber %s unsubscribed", subscriberName)
	return nil
}

// Publish implements the EventBus interface (synchronous).
func (b *eventBus) Publish(ctx context.Context, event Event) error {
	if b.shutdownFlag.Load() {
		return ErrEventBusShutdown
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	// Update statistics
	atomic.AddUint64(&b.stats.TotalEvents, 1)
	b.stats.ByType[event.Type()]++

	// Find matching subscribers
	subscribers := b.findSubscribers(event.Type())

	if len(subscribers) == 0 {
		return nil
	}

	log.Printf("[EventBus] Publishing event %s to %d subscribers", event.Type(), len(subscribers))

	// Synchronously call all subscribers
	for _, subscriber := range subscribers {
		if !subscriber.IsEnabled() {
			continue
		}

		b.wg.Add(1)
		go func(sub Subscriber) {
			defer b.wg.Done()
			if err := sub.OnEvent(ctx, event); err != nil {
				log.Printf("[EventBus] Subscriber %s failed to handle event %s: %v",
					sub.Name(), event.Type(), err)
				atomic.AddUint64(&b.stats.TotalErrors, 1)
			}
		}(subscriber)
	}

	return nil
}

// PublishAsync implements the EventBus interface.
func (b *eventBus) PublishAsync(ctx context.Context, event Event) <-chan error {
	errChan := make(chan error, 1)
	go func() {
		errChan <- b.Publish(ctx, event)
	}()
	return errChan
}

// findSubscribers finds matching subscribers.
func (b *eventBus) findSubscribers(eventType string) []Subscriber {
	var result []Subscriber

	// Exact match
	if names, ok := b.topicIndex[eventType]; ok {
		for _, name := range names {
			if sub, exists := b.subscribers[name]; exists {
				result = append(result, sub)
			}
		}
	}

	// Wildcard match "*"
	if names, ok := b.topicIndex["*"]; ok {
		for _, name := range names {
			sub := b.subscribers[name]
			// Avoid duplicate addition
			alreadyAdded := false
			for _, existing := range result {
				if existing == sub {
					alreadyAdded = true
					break
				}
			}
			if !alreadyAdded {
				result = append(result, sub)
			}
		}
	}

	// Prefix match (e.g., "agent.*")
	for topic, names := range b.topicIndex {
		if len(topic) > 2 && topic[len(topic)-2:] == ".*" {
			prefix := topic[:len(topic)-2]
			if strings.HasPrefix(eventType, prefix) {
				for _, name := range names {
					sub := b.subscribers[name]
					// Avoid duplicate addition
					alreadyAdded := false
					for _, existing := range result {
						if existing == sub {
							alreadyAdded = true
							break
						}
					}
					if !alreadyAdded {
						result = append(result, sub)
					}
				}
			}
		}
	}

	return result
}

// Shutdown implements the EventBus interface.
func (b *eventBus) Shutdown(ctx context.Context) error {
	if !b.shutdownFlag.CompareAndSwap(false, true) {
		return nil // Already shut down
	}

	log.Printf("[EventBus] Shutting down...")

	b.mu.Lock()
	defer b.mu.Unlock()

	// Shutdown all subscribers
	for name, subscriber := range b.subscribers {
		if err := subscriber.Shutdown(ctx); err != nil {
			log.Printf("[EventBus] Failed to shutdown subscriber %s: %v", name, err)
		}
	}

	// Wait for all event handling to complete
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Printf("[EventBus] Shutdown completed")
		return nil
	case <-ctx.Done():
		log.Printf("[EventBus] Shutdown timeout")
		return ctx.Err()
	}
}

// Stats implements the EventBus interface.
func (b *eventBus) Stats() Stats {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return Stats{
		TotalSubscribers: len(b.subscribers),
		TotalEvents:      atomic.LoadUint64(&b.stats.TotalEvents),
		TotalErrors:      atomic.LoadUint64(&b.stats.TotalErrors),
		ByType:           b.stats.ByType,
	}
}

// Global event bus instance
var globalBus EventBus
var once sync.Once

// GetGlobalBus returns the global event bus (singleton).
func GetGlobalBus() EventBus {
	once.Do(func() {
		globalBus = NewEventBus()
	})
	return globalBus
}
