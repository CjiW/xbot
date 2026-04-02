package agent

import (
	"testing"

	"xbot/bus"
)

// ==================== Background Task Notification ====================

func TestInjectInbound_IsCronFalse(t *testing.T) {
	// injectInbound must NOT set IsCron=true, otherwise processMessage
	// routes through processCronMessage which skips persistence.

	a := &Agent{
		bus: bus.NewMessageBus(),
	}

	go func() {
		a.injectInbound("cli", "test-chat", "system", "bg task done")
	}()

	msg := <-a.bus.Inbound

	if msg.IsCron {
		t.Error("injectInbound should set IsCron=false, got true — this would bypass persistence")
	}
	if msg.Channel != "cli" {
		t.Errorf("Channel = %q, want %q", msg.Channel, "cli")
	}
	if msg.ChatID != "test-chat" {
		t.Errorf("ChatID = %q, want %q", msg.ChatID, "test-chat")
	}
	if msg.Content != "bg task done" {
		t.Errorf("Content = %q, want %q", msg.Content, "bg task done")
	}
	if msg.RequestID == "" {
		t.Error("RequestID should be set")
	}
}
