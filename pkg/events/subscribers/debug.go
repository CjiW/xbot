package subscribers

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"xbot/pkg/events"
)

// DebugSubscriber is the debug subscriber.
type DebugSubscriber struct {
	*events.BaseSubscriber
	debugFile *os.File
}

// NewDebugSubscriber creates a debug subscriber.
func NewDebugSubscriber(filePath string) (*DebugSubscriber, error) {
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	return &DebugSubscriber{
		BaseSubscriber: events.NewBaseSubscriber("debug", []string{"*"}),
		debugFile:      file,
	}, nil
}

// OnEvent implements the Subscriber interface.
func (s *DebugSubscriber) OnEvent(ctx context.Context, event events.Event) error {
	debugLine := fmt.Sprintf("[%s] %s %s\n",
		event.Timestamp().Format(time.RFC3339Nano),
		event.Type(),
		event.ID(),
	)

	if _, err := s.debugFile.WriteString(debugLine); err != nil {
		log.Printf("[Debug] Failed to write debug line: %v", err)
		return err
	}

	return nil
}

// Shutdown implements the Subscriber interface.
func (s *DebugSubscriber) Shutdown(ctx context.Context) error {
	if s.debugFile != nil {
		return s.debugFile.Close()
	}
	return nil
}
