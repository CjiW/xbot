package subscribers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"xbot/pkg/events"
)

// LoggerSubscriber is the logging subscriber.
type LoggerSubscriber struct {
	*events.BaseSubscriber
	logLevel string // "debug" | "info" | "warn" | "error"
}

// NewLoggerSubscriber creates a logging subscriber.
func NewLoggerSubscriber(logLevel string) *LoggerSubscriber {
	return &LoggerSubscriber{
		BaseSubscriber: events.NewBaseSubscriber("logger", []string{"*"}), // Subscribe to all events
		logLevel:       logLevel,
	}
}

// OnEvent implements the Subscriber interface.
func (s *LoggerSubscriber) OnEvent(ctx context.Context, event events.Event) error {
	payload, _ := json.MarshalIndent(event.Payload(), "", "  ")

	logMsg := fmt.Sprintf("[Event] Type=%s Source=%s ID=%s Time=%s\nPayload:\n%s",
		event.Type(),
		event.Source(),
		event.ID(),
		event.Timestamp().Format(time.RFC3339),
		string(payload),
	)

	switch s.logLevel {
	case "debug":
		log.Print(logMsg)
	case "info":
		if event.Type() != "system.debug" {
			log.Print(logMsg)
		}
	case "warn":
		if event.Type() == "system.warn" || event.Type() == "system.error" {
			log.Print(logMsg)
		}
	case "error":
		if event.Type() == "system.error" {
			log.Print(logMsg)
		}
	}

	return nil
}
