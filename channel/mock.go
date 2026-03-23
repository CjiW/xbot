package channel

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"xbot/bus"
)

// MockChannel implements the Channel interface for integration testing.
// It records all outbound messages in memory and provides helpers to
// simulate inbound messages. No external service dependencies.
type MockChannel struct {
	name    string
	msgBus  *bus.MessageBus
	outbox  []bus.OutboundMessage
	mu      sync.RWMutex
	stopped chan struct{}
	stopMu  sync.Once
}

// NewMockChannel creates a MockChannel with the given name and MessageBus.
func NewMockChannel(name string, msgBus *bus.MessageBus) *MockChannel {
	return &MockChannel{
		name:    name,
		msgBus:  msgBus,
		stopped: make(chan struct{}),
	}
}

// Name returns the channel name.
func (mc *MockChannel) Name() string { return mc.name }

// Start starts the mock channel (no-op, always succeeds).
func (mc *MockChannel) Start() error { return nil }

// Stop stops the mock channel.
func (mc *MockChannel) Stop() {
	mc.stopMu.Do(func() { close(mc.stopped) })
}

// Send records the outbound message in the internal outbox.
func (mc *MockChannel) Send(msg bus.OutboundMessage) (string, error) {
	mc.mu.Lock()
	mc.outbox = append(mc.outbox, msg)
	mc.mu.Unlock()
	return "mock_msg_" + generateMockID(), nil
}

// --- Simulate inbound messages ---

// SimulateMessage simulates a user text message pushed into the Inbound channel.
func (mc *MockChannel) SimulateMessage(content string) {
	mc.msgBus.Inbound <- bus.InboundMessage{
		Channel:    mc.name,
		SenderID:   "mock_user",
		SenderName: "Mock User",
		ChatID:     "mock_chat",
		ChatType:   "p2p",
		Content:    content,
		Time:       time.Now(),
		RequestID:  generateMockRequestID(),
	}
}

// SimulateMedia simulates an inbound message with media file paths.
func (mc *MockChannel) SimulateMedia(media []string) {
	mc.msgBus.Inbound <- bus.InboundMessage{
		Channel:    mc.name,
		SenderID:   "mock_user",
		SenderName: "Mock User",
		ChatID:     "mock_chat",
		ChatType:   "p2p",
		Content:    "",
		Media:      media,
		Time:       time.Now(),
		RequestID:  generateMockRequestID(),
	}
}

// SimulateCard simulates a card interaction (button click, form submit, etc.).
// The action and value are stored in the Metadata map.
func (mc *MockChannel) SimulateCard(action, value string) {
	mc.msgBus.Inbound <- bus.InboundMessage{
		Channel:    mc.name,
		SenderID:   "mock_user",
		SenderName: "Mock User",
		ChatID:     "mock_chat",
		ChatType:   "p2p",
		Content:    value,
		Time:       time.Now(),
		RequestID:  generateMockRequestID(),
		Metadata: map[string]string{
			"card_action": action,
			"card_value":  value,
		},
	}
}

// SimulateInbound pushes an arbitrary InboundMessage into the bus.
func (mc *MockChannel) SimulateInbound(msg bus.InboundMessage) {
	if msg.RequestID == "" {
		msg.RequestID = generateMockRequestID()
	}
	mc.msgBus.Inbound <- msg
}

// --- Assertion helpers ---

// Outbox returns a copy of all recorded outbound messages.
func (mc *MockChannel) Outbox() []bus.OutboundMessage {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	result := make([]bus.OutboundMessage, len(mc.outbox))
	copy(result, mc.outbox)
	return result
}

// LastOutbound returns the most recent outbound message, or nil if empty.
func (mc *MockChannel) LastOutbound() *bus.OutboundMessage {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	if len(mc.outbox) == 0 {
		return nil
	}
	msg := mc.outbox[len(mc.outbox)-1]
	return &msg
}

// WaitForOutbound waits until at least one outbound message is recorded or timeout.
func (mc *MockChannel) WaitForOutbound(timeout time.Duration) bool {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(timeout)
	for {
		select {
		case <-ticker.C:
			if mc.OutboxCount() > 0 {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// Clear removes all recorded outbound messages.
func (mc *MockChannel) Clear() {
	mc.mu.Lock()
	mc.outbox = nil
	mc.mu.Unlock()
}

// OutboxCount returns the number of recorded outbound messages.
func (mc *MockChannel) OutboxCount() int {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return len(mc.outbox)
}

// --- internal helpers ---

func generateMockID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func generateMockRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
