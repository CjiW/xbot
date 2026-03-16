package agent

import (
	"context"
	"testing"

	"xbot/bus"
	"xbot/tools"
)

func TestCallChain_CanSpawn(t *testing.T) {
	tests := []struct {
		name    string
		chain   []string
		target  string
		wantErr bool
	}{
		{
			name:    "normal spawn from main",
			chain:   []string{"main"},
			target:  "code-reviewer",
			wantErr: false,
		},
		{
			name:    "depth 2 spawn",
			chain:   []string{"main", "main/code-reviewer"},
			target:  "explorer",
			wantErr: false,
		},
		{
			name:    "max depth reached",
			chain:   []string{"main", "main/a", "main/a/b"},
			target:  "c",
			wantErr: true,
		},
		{
			name:    "circular call",
			chain:   []string{"main", "main/code-reviewer"},
			target:  "code-reviewer",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc := &CallChain{Chain: tt.chain}
			err := cc.CanSpawn(tt.target)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestCallChain_Spawn(t *testing.T) {
	cc := &CallChain{Chain: []string{"main"}}
	child := cc.Spawn("code-reviewer")

	if len(child.Chain) != 2 {
		t.Fatalf("expected chain length 2, got %d", len(child.Chain))
	}
	if child.Chain[0] != "main" {
		t.Errorf("chain[0] = %q, want %q", child.Chain[0], "main")
	}
	if child.Chain[1] != "main/code-reviewer" {
		t.Errorf("chain[1] = %q, want %q", child.Chain[1], "main/code-reviewer")
	}

	// Original should be unchanged
	if len(cc.Chain) != 1 {
		t.Errorf("original chain modified: %v", cc.Chain)
	}
}

func TestCallChain_Context(t *testing.T) {
	ctx := context.Background()

	// Default chain
	cc := CallChainFromContext(ctx)
	if cc.Current() != "main" {
		t.Errorf("default Current() = %q, want %q", cc.Current(), "main")
	}
	if cc.Depth() != 1 {
		t.Errorf("default Depth() = %d, want 1", cc.Depth())
	}

	// Inject chain
	custom := &CallChain{Chain: []string{"main", "main/cr"}}
	ctx = WithCallChain(ctx, custom)
	got := CallChainFromContext(ctx)
	if got.Current() != "main/cr" {
		t.Errorf("Current() = %q, want %q", got.Current(), "main/cr")
	}
	if got.Depth() != 2 {
		t.Errorf("Depth() = %d, want 2", got.Depth())
	}
}

func TestSpawnAgentAdapter(t *testing.T) {
	var capturedMsg bus.InboundMessage

	adapter := &spawnAgentAdapter{
		spawnFn: func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			capturedMsg = msg
			return &bus.OutboundMessage{
				Content: "task completed",
			}, nil
		},
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_xxx",
		senderID: "ou_xxx",
	}

	parentCtx := &tools.ToolContext{
		Ctx:        context.Background(),
		SenderID:   "ou_xxx",
		SenderName: "Test User",
		ChatID:     "oc_xxx",
	}

	result, err := adapter.RunSubAgent(parentCtx, "review this code", "You are a code reviewer.", []string{"Shell", "Read"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "task completed" {
		t.Errorf("result = %q, want %q", result, "task completed")
	}

	// Verify InboundMessage was constructed correctly
	if capturedMsg.Channel != "agent" {
		t.Errorf("Channel = %q, want %q", capturedMsg.Channel, "agent")
	}
	if capturedMsg.Content != "review this code" {
		t.Errorf("Content = %q, want %q", capturedMsg.Content, "review this code")
	}
	if capturedMsg.ParentAgentID != "main" {
		t.Errorf("ParentAgentID = %q, want %q", capturedMsg.ParentAgentID, "main")
	}
	if capturedMsg.SystemPrompt != "You are a code reviewer." {
		t.Errorf("SystemPrompt = %q", capturedMsg.SystemPrompt)
	}
	if len(capturedMsg.AllowedTools) != 2 {
		t.Errorf("AllowedTools = %v, want [Shell Read]", capturedMsg.AllowedTools)
	}
	if !capturedMsg.IsFromAgent() {
		t.Error("expected IsFromAgent() = true")
	}
	if capturedMsg.OriginChannel() != "feishu" {
		t.Errorf("OriginChannel() = %q, want %q", capturedMsg.OriginChannel(), "feishu")
	}
	if capturedMsg.OriginChatID() != "oc_xxx" {
		t.Errorf("OriginChatID() = %q, want %q", capturedMsg.OriginChatID(), "oc_xxx")
	}
	if capturedMsg.OriginSenderID() != "ou_xxx" {
		t.Errorf("OriginSenderID() = %q, want %q", capturedMsg.OriginSenderID(), "ou_xxx")
	}

	// Verify unified addressing
	if !capturedMsg.From.IsIM() {
		t.Errorf("From should be IM address, got %v", capturedMsg.From)
	}
	if !capturedMsg.To.IsAgent() {
		t.Errorf("To should be Agent address, got %v", capturedMsg.To)
	}
}

func TestSpawnAgentAdapter_ErrorPropagation(t *testing.T) {
	adapter := &spawnAgentAdapter{
		spawnFn: func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			return &bus.OutboundMessage{
				Content: "partial result",
				Error:   context.Canceled,
			}, nil
		},
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_xxx",
		senderID: "ou_xxx",
	}

	parentCtx := &tools.ToolContext{
		Ctx: context.Background(),
	}

	result, err := adapter.RunSubAgent(parentCtx, "task", "", nil)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if result != "partial result" {
		t.Errorf("result = %q, want %q", result, "partial result")
	}
}

func TestBuildToolContext(t *testing.T) {
	called := false
	cfg := &RunConfig{
		AgentID:    "main",
		Channel:    "feishu",
		ChatID:     "oc_xxx",
		SenderID:   "ou_xxx",
		SenderName: "Test",
		SendFunc: func(ch, cid, content string) error {
			return nil
		},
		SpawnAgent: func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			called = true
			return &bus.OutboundMessage{Content: "ok"}, nil
		},
	}

	tc := buildToolContext(context.Background(), cfg)

	if tc.AgentID != "main" {
		t.Errorf("AgentID = %q", tc.AgentID)
	}
	if tc.Channel != "feishu" {
		t.Errorf("Channel = %q", tc.Channel)
	}
	if tc.Manager == nil {
		t.Fatal("Manager should not be nil when SpawnAgent is set")
	}

	// Verify Manager works
	_, _ = tc.Manager.RunSubAgent(tc, "test", "prompt", nil)
	if !called {
		t.Error("SpawnAgent was not called through Manager")
	}
}

func TestBuildToolContext_WithExtras(t *testing.T) {
	cfg := &RunConfig{
		AgentID: "main",
		ToolContextExtras: &ToolContextExtras{
			TenantID: 42,
		},
	}

	tc := buildToolContext(context.Background(), cfg)
	if tc.TenantID != 42 {
		t.Errorf("TenantID = %d, want 42", tc.TenantID)
	}
}

func TestBuildToolContext_NilExtras(t *testing.T) {
	cfg := &RunConfig{
		AgentID: "main",
	}

	tc := buildToolContext(context.Background(), cfg)
	if tc.TenantID != 0 {
		t.Errorf("TenantID = %d, want 0", tc.TenantID)
	}
	if tc.Manager != nil {
		t.Error("Manager should be nil when SpawnAgent is nil")
	}
}
