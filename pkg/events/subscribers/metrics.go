package subscribers

import (
	"context"
	"sync/atomic"

	"xbot/pkg/events"
)

// MetricsSubscriber is the metrics subscriber.
type MetricsSubscriber struct {
	*events.BaseSubscriber
	totalEvents   uint64
	eventsByType  map[string]uint64
	toolsCalled   uint64
	agentsCreated uint64
	errors        uint64
}

// NewMetricsSubscriber creates a metrics subscriber.
func NewMetricsSubscriber() *MetricsSubscriber {
	return &MetricsSubscriber{
		BaseSubscriber: events.NewBaseSubscriber("metrics", []string{"*"}),
		eventsByType:   make(map[string]uint64),
	}
}

// OnEvent implements the Subscriber interface.
func (s *MetricsSubscriber) OnEvent(ctx context.Context, event events.Event) error {
	atomic.AddUint64(&s.totalEvents, 1)
	s.eventsByType[event.Type()]++

	switch event.Type() {
	case events.EventTypeToolCalled:
		atomic.AddUint64(&s.toolsCalled, 1)
	case events.EventTypeAgentCreated:
		atomic.AddUint64(&s.agentsCreated, 1)
	case "system.error", events.EventTypeToolFailed:
		atomic.AddUint64(&s.errors, 1)
	}

	return nil
}

// GetStats returns the statistics.
func (s *MetricsSubscriber) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"total_events":   atomic.LoadUint64(&s.totalEvents),
		"events_by_type": s.eventsByType,
		"tools_called":   atomic.LoadUint64(&s.toolsCalled),
		"agents_created": atomic.LoadUint64(&s.agentsCreated),
		"errors":         atomic.LoadUint64(&s.errors),
	}
}
