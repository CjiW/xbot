package channel

import (
	"strings"
	"testing"
	"time"

	"xbot/bus"
)

func TestMockChannel_Name(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test_mock", msgBus)
	if mc.Name() != "test_mock" {
		t.Errorf("Name() = %q, want %q", mc.Name(), "test_mock")
	}
}

func TestMockChannel_StartStop(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test", msgBus)

	if err := mc.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Stop should be idempotent
	mc.Stop()
	mc.Stop() // second call should not panic
}

func TestMockChannel_SendRecordsOutbound(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test", msgBus)

	msg := bus.OutboundMessage{
		Channel: "test",
		ChatID:  "chat1",
		Content: "hello world",
	}

	msgID, err := mc.Send(msg)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}
	if !strings.HasPrefix(msgID, "mock_msg_") {
		t.Errorf("msgID = %q, want prefix %q", msgID, "mock_msg_")
	}

	if mc.OutboxCount() != 1 {
		t.Fatalf("OutboxCount() = %d, want 1", mc.OutboxCount())
	}

	outbox := mc.Outbox()
	if outbox[0].Content != "hello world" {
		t.Errorf("outbox[0].Content = %q, want %q", outbox[0].Content, "hello world")
	}
}

func TestMockChannel_MultipleSends(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test", msgBus)

	for i := 0; i < 5; i++ {
		_, err := mc.Send(bus.OutboundMessage{
			Channel: "test",
			Content: "msg" + string(rune('0'+i)),
		})
		if err != nil {
			t.Fatalf("Send() %d failed: %v", i, err)
		}
	}

	if mc.OutboxCount() != 5 {
		t.Errorf("OutboxCount() = %d, want 5", mc.OutboxCount())
	}

	last := mc.LastOutbound()
	if last == nil {
		t.Fatal("LastOutbound() = nil, want non-nil")
	}
	if last.Content != "msg4" {
		t.Errorf("LastOutbound().Content = %q, want %q", last.Content, "msg4")
	}
}

func TestMockChannel_Clear(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test", msgBus)

	mc.Send(bus.OutboundMessage{Channel: "test", Content: "a"})
	mc.Send(bus.OutboundMessage{Channel: "test", Content: "b"})
	if mc.OutboxCount() != 2 {
		t.Fatalf("pre-clear: OutboxCount() = %d, want 2", mc.OutboxCount())
	}

	mc.Clear()
	if mc.OutboxCount() != 0 {
		t.Errorf("post-clear: OutboxCount() = %d, want 0", mc.OutboxCount())
	}
	if mc.LastOutbound() != nil {
		t.Error("LastOutbound() should be nil after Clear()")
	}
}

func TestMockChannel_SimulateMessage(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test", msgBus)

	go mc.SimulateMessage("hello from user")

	select {
	case msg := <-msgBus.Inbound:
		if msg.Channel != "test" {
			t.Errorf("inbound.Channel = %q, want %q", msg.Channel, "test")
		}
		if msg.Content != "hello from user" {
			t.Errorf("inbound.Content = %q, want %q", msg.Content, "hello from user")
		}
		if msg.SenderID != "mock_user" {
			t.Errorf("inbound.SenderID = %q, want %q", msg.SenderID, "mock_user")
		}
		if msg.RequestID == "" {
			t.Error("inbound.RequestID should not be empty")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestMockChannel_SimulateMedia(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test", msgBus)

	go mc.SimulateMedia([]string{"/path/to/file.pdf", "/path/to/image.png"})

	select {
	case msg := <-msgBus.Inbound:
		if len(msg.Media) != 2 {
			t.Fatalf("len(media) = %d, want 2", len(msg.Media))
		}
		if msg.Media[0] != "/path/to/file.pdf" {
			t.Errorf("media[0] = %q, want %q", msg.Media[0], "/path/to/file.pdf")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestMockChannel_SimulateCard(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test", msgBus)

	go mc.SimulateCard("button_click", "approve")

	select {
	case msg := <-msgBus.Inbound:
		if msg.Metadata["card_action"] != "button_click" {
			t.Errorf("metadata[card_action] = %q, want %q", msg.Metadata["card_action"], "button_click")
		}
		if msg.Metadata["card_value"] != "approve" {
			t.Errorf("metadata[card_value] = %q, want %q", msg.Metadata["card_value"], "approve")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestMockChannel_SimulateInbound(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test", msgBus)

	custom := bus.InboundMessage{
		Channel:  "custom",
		Content:  "custom content",
		SenderID: "custom_user",
	}

	go mc.SimulateInbound(custom)

	select {
	case msg := <-msgBus.Inbound:
		if msg.Content != "custom content" {
			t.Errorf("inbound.Content = %q, want %q", msg.Content, "custom content")
		}
		if msg.SenderID != "custom_user" {
			t.Errorf("inbound.SenderID = %q, want %q", msg.SenderID, "custom_user")
		}
		if msg.RequestID == "" {
			t.Error("RequestID should be auto-generated when empty")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestMockChannel_WaitForOutbound(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test", msgBus)

	// No messages yet — should timeout
	if mc.WaitForOutbound(50 * time.Millisecond) {
		t.Error("WaitForOutbound should return false when no messages")
	}

	// Send a message in a goroutine
	go func() {
		time.Sleep(20 * time.Millisecond)
		mc.Send(bus.OutboundMessage{Channel: "test", Content: "delayed"})
	}()

	if !mc.WaitForOutbound(2 * time.Second) {
		t.Error("WaitForOutbound should return true after Send()")
	}
}

func TestMockChannel_OutboxIsSnapshot(t *testing.T) {
	msgBus := bus.NewMessageBus()
	mc := NewMockChannel("test", msgBus)

	mc.Send(bus.OutboundMessage{Channel: "test", Content: "first"})
	outbox1 := mc.Outbox()

	mc.Send(bus.OutboundMessage{Channel: "test", Content: "second"})
	outbox2 := mc.Outbox()

	// outbox1 should be a snapshot, not affected by subsequent sends
	if len(outbox1) != 1 {
		t.Errorf("len(outbox1) = %d, want 1 (snapshot)", len(outbox1))
	}
	if len(outbox2) != 2 {
		t.Errorf("len(outbox2) = %d, want 2", len(outbox2))
	}
}

func TestMockChannel_ImplementsChannelInterface(t *testing.T) {
	// Compile-time check
	var _ Channel = (*MockChannel)(nil)
}
