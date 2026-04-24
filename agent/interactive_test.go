package agent

import (
	"testing"

	"xbot/bus"
)

func TestInteractiveKey(t *testing.T) {
	tests := []struct {
		channel, chatID, roleName, instance, want string
	}{
		{"feishu", "oc_xxx", "code-reviewer", "", "feishu:oc_xxx/code-reviewer"},
		{"cli", "direct", "writer", "", "cli:direct/writer"},
		{"", "", "test", "", ":/test"},
		{"feishu", "oc_xxx", "brainstorm", "1", "feishu:oc_xxx/brainstorm:1"},
		{"feishu", "oc_xxx", "brainstorm", "architect", "feishu:oc_xxx/brainstorm:architect"},
	}
	for _, tt := range tests {
		got := interactiveKey(tt.channel, tt.chatID, tt.roleName, tt.instance)
		if got != tt.want {
			t.Errorf("interactiveKey(%q, %q, %q, %q) = %q, want %q", tt.channel, tt.chatID, tt.roleName, tt.instance, got, tt.want)
		}
	}
}

func TestParseInteractiveKeyChatID(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"CLI absolute path", "cli:/home/user/workspace/explore:oneshot-explore-1234", "/home/user/workspace"},
		{"Feishu chatID", "feishu:oc_abc123/explore:session-1", "oc_abc123"},
		{"Web chatID", "web:senderID/explore:inst", "senderID"},
		{"multi-slash path", "cli:/a/b/c/d/explore:oneshot-explore-9876", "/a/b/c/d"},
		{"empty chatID", "cli:/explore:inst", ""},
		{"no slash", "cli:explore:inst", ""},
		{"no colon", "cli/explore:inst", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseInteractiveKeyChatID(tt.key)
			if got != tt.want {
				t.Errorf("parseInteractiveKeyChatID(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
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
