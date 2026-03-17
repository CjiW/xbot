package agent

import (
	"context"
	"fmt"
	"testing"

	"xbot/bus"
	"xbot/tools"
)

func TestSpawnAgentAdapter_InteractiveSpawn_NilCallback(t *testing.T) {
	adapter := &spawnAgentAdapter{
		spawnFn: func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			return &bus.OutboundMessage{Content: "ok"}, nil
		},
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_123",
		senderID: "ou_456",
	}

	// No interactive callbacks → should return error
	_, err := adapter.SpawnInteractive(&tools.ToolContext{
		Ctx:      context.Background(),
		SenderID: "ou_456",
		ChatID:   "oc_123",
		Channel:  "feishu",
	}, "task", "reviewer", "You are a reviewer", nil, tools.SubAgentCapabilities{})
	if err == nil {
		t.Fatal("expected error when interactive callbacks are nil")
	}
	if err.Error() != "interactive mode not supported" {
		t.Errorf("error = %q, want %q", err.Error(), "interactive mode not supported")
	}
}

func TestSpawnAgentAdapter_InteractiveSend_NilCallback(t *testing.T) {
	adapter := &spawnAgentAdapter{
		parentID: "main",
	}
	_, err := adapter.SendInteractive(&tools.ToolContext{
		Ctx: context.Background(),
	}, "task", "reviewer", "", nil, tools.SubAgentCapabilities{})
	if err == nil {
		t.Fatal("expected error when interactive callbacks are nil")
	}
}

func TestSpawnAgentAdapter_InteractiveUnload_NilCallback(t *testing.T) {
	adapter := &spawnAgentAdapter{
		parentID: "main",
	}
	err := adapter.UnloadInteractive(&tools.ToolContext{
		Ctx: context.Background(),
	}, "reviewer")
	if err == nil {
		t.Fatal("expected error when interactive callbacks are nil")
	}
}

func TestSpawnAgentAdapter_InteractiveSpawn_Success(t *testing.T) {
	var capturedRole string
	adapter := &spawnAgentAdapter{
		spawnFn: func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			return &bus.OutboundMessage{Content: "spawned"}, nil
		},
		interactiveSpawnFn: func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			capturedRole = roleName
			return &bus.OutboundMessage{Content: "interactive spawned"}, nil
		},
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_123",
		senderID: "ou_456",
	}

	result, err := adapter.SpawnInteractive(&tools.ToolContext{
		Ctx:      context.Background(),
		SenderID: "ou_456",
		Channel:  "feishu",
		ChatID:   "oc_123",
	}, "review my code", "reviewer", "You are a code reviewer", nil, tools.SubAgentCapabilities{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "interactive spawned" {
		t.Errorf("result = %q, want %q", result, "interactive spawned")
	}
	if capturedRole != "reviewer" {
		t.Errorf("capturedRole = %q, want %q", capturedRole, "reviewer")
	}
}

func TestSpawnAgentAdapter_InteractiveSend_Success(t *testing.T) {
	adapter := &spawnAgentAdapter{
		interactiveSendFn: func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			return &bus.OutboundMessage{Content: "sent " + msg.Content}, nil
		},
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_123",
		senderID: "ou_456",
	}

	result, err := adapter.SendInteractive(&tools.ToolContext{
		Ctx:      context.Background(),
		SenderID: "ou_456",
		Channel:  "feishu",
		ChatID:   "oc_123",
	}, "fix this bug", "writer", "", nil, tools.SubAgentCapabilities{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "sent fix this bug" {
		t.Errorf("result = %q", result)
	}
}

func TestSpawnAgentAdapter_InteractiveUnload_Success(t *testing.T) {
	unloaded := false
	adapter := &spawnAgentAdapter{
		interactiveUnloadFn: func(ctx context.Context, roleName string) error {
			unloaded = true
			return nil
		},
		parentID: "main",
	}

	err := adapter.UnloadInteractive(&tools.ToolContext{
		Ctx: context.Background(),
	}, "reviewer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !unloaded {
		t.Error("expected unloadFn to be called")
	}
}

func TestSpawnAgentAdapter_InteractiveUnload_Error(t *testing.T) {
	adapter := &spawnAgentAdapter{
		interactiveUnloadFn: func(ctx context.Context, roleName string) error {
			return fmt.Errorf("no such session")
		},
		parentID: "main",
	}

	err := adapter.UnloadInteractive(&tools.ToolContext{
		Ctx: context.Background(),
	}, "reviewer")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "no such session" {
		t.Errorf("error = %q", err.Error())
	}
}

func TestSpawnAgentAdapter_BuildMsg_Interactive(t *testing.T) {
	adapter := &spawnAgentAdapter{
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_abc",
		senderID: "ou_xyz",
	}

	msg := adapter.buildMsg(&tools.ToolContext{
		Ctx:        context.Background(),
		SenderID:   "ou_xyz",
		SenderName: "Test User",
		Channel:    "feishu",
		ChatID:     "oc_abc",
	}, "do something", "reviewer", "You are reviewer", []string{"Read", "Grep"}, tools.SubAgentCapabilities{Memory: true}, true)

	// Check interactive flag in metadata
	if msg.Metadata["interactive"] != "true" {
		t.Errorf("metadata[interactive] = %q, want %q", msg.Metadata["interactive"], "true")
	}
	// Check origin fields preserved
	if msg.Metadata["origin_channel"] != "feishu" {
		t.Errorf("metadata[origin_channel] = %q", msg.Metadata["origin_channel"])
	}
	if msg.Metadata["origin_chat_id"] != "oc_abc" {
		t.Errorf("metadata[origin_chat_id] = %q", msg.Metadata["origin_chat_id"])
	}
	// Check capabilities
	if !msg.Capabilities["memory"] {
		t.Error("expected memory capability")
	}
	// Check allowed tools
	if len(msg.AllowedTools) != 2 {
		t.Errorf("AllowedTools = %v, want 2 items", msg.AllowedTools)
	}
}

func TestSpawnAgentAdapter_BuildMsg_NonInteractive(t *testing.T) {
	adapter := &spawnAgentAdapter{
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_abc",
		senderID: "ou_xyz",
	}

	msg := adapter.buildMsg(&tools.ToolContext{
		Ctx:      context.Background(),
		SenderID: "ou_xyz",
	}, "do something", "reviewer", "", nil, tools.SubAgentCapabilities{}, false)

	if msg.Metadata["interactive"] == "true" {
		t.Error("interactive flag should not be set for non-interactive mode")
	}
}
