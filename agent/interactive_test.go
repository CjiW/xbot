package agent

import (
	"testing"

	"xbot/bus"
)

func TestInteractiveKey(t *testing.T) {
	tests := []struct {
		channel, chatID, roleName, want string
	}{
		{"feishu", "oc_xxx", "code-reviewer", "feishu:oc_xxx/code-reviewer"},
		{"cli", "direct", "writer", "cli:direct/writer"},
		{"", "", "test", ":/test"},
	}
	for _, tt := range tests {
		got := interactiveKey(tt.channel, tt.chatID, tt.roleName)
		if got != tt.want {
			t.Errorf("interactiveKey(%q, %q, %q) = %q, want %q", tt.channel, tt.chatID, tt.roleName, got, tt.want)
		}
	}
}

func TestResolveOriginIDs_WithMetadata(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "agent",
		SenderID: "sub_agent",
		ChatID:   "agent_chat",
		Metadata: map[string]string{
			"origin_channel": "feishu",
			"origin_chat_id": "oc_abc123",
			"origin_sender":  "ou_xyz789",
		},
	}
	ch, chatID, sender := resolveOriginIDs(msg)
	if ch != "feishu" {
		t.Errorf("channel = %q, want %q", ch, "feishu")
	}
	if chatID != "oc_abc123" {
		t.Errorf("chatID = %q, want %q", chatID, "oc_abc123")
	}
	if sender != "ou_xyz789" {
		t.Errorf("sender = %q, want %q", sender, "ou_xyz789")
	}
}

func TestResolveOriginIDs_FallbackToTopLevel(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "feishu",
		SenderID: "ou_direct",
		ChatID:   "oc_direct",
		Metadata: nil,
	}
	ch, chatID, sender := resolveOriginIDs(msg)
	if ch != "feishu" {
		t.Errorf("channel = %q, want %q", ch, "feishu")
	}
	if chatID != "oc_direct" {
		t.Errorf("chatID = %q, want %q", chatID, "oc_direct")
	}
	if sender != "ou_direct" {
		t.Errorf("sender = %q, want %q", sender, "ou_direct")
	}
}

func TestResolveOriginIDs_PartialMetadata(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "agent",
		SenderID: "sub",
		ChatID:   "sub_chat",
		Metadata: map[string]string{
			"origin_channel": "feishu",
			// origin_chat_id and origin_sender missing → should fallback
		},
	}
	ch, chatID, sender := resolveOriginIDs(msg)
	if ch != "feishu" {
		t.Errorf("channel = %q, want %q", ch, "feishu")
	}
	if chatID != "sub_chat" {
		t.Errorf("chatID = %q, want %q (fallback)", chatID, "sub_chat")
	}
	if sender != "sub" {
		t.Errorf("sender = %q, want %q (fallback)", sender, "sub")
	}
}
