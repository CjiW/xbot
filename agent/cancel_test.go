package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"xbot/llm"
	"xbot/tools"
)

func TestCancelNotRegisteredAsCommand(t *testing.T) {
	// /cancel is intercepted in Run(), not registered as a Command.
	// It should NOT match as a command in the registry.
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	if r.IsCommand("/cancel") {
		t.Error("IsCommand(/cancel) = true, want false — /cancel is handled in Run(), not as a registered command")
	}
	if r.IsCommand("/CANCEL") {
		t.Error("IsCommand(/CANCEL) = true, want false")
	}
}

func TestChatCancelCh_BasicSignaling(t *testing.T) {
	// Test that the cancel channel mechanism works correctly
	var cancelMap sync.Map

	cancelKey := "feishu:chat123:user456"
	cancelCh := make(chan struct{}, 1)
	cancelMap.Store(cancelKey, cancelCh)

	// Sending cancel signal should succeed
	if ch, ok := cancelMap.Load(cancelKey); ok {
		select {
		case ch.(chan struct{}) <- struct{}{}:
			// OK
		default:
			t.Error("Failed to send cancel signal")
		}
	} else {
		t.Error("Cancel channel not found")
	}

	// Second send should not block (channel already has a signal, default case)
	if ch, ok := cancelMap.Load(cancelKey); ok {
		select {
		case ch.(chan struct{}) <- struct{}{}:
			t.Error("Second cancel signal should not succeed (channel full)")
		default:
			// OK — expected
		}
	}

	// Different key should not find a channel
	wrongKey := "feishu:chat123:otherUser"
	if _, ok := cancelMap.Load(wrongKey); ok {
		t.Error("Should not find cancel channel for different user")
	}
}

func TestCancelKeyIsPerChat(t *testing.T) {
	// Verify that cancel keys are per-chat (channel:chatID), not per-sender.
	// One chat has at most one active Run at a time.
	var cancelMap sync.Map

	chatA := "feishu:chatA"
	chatB := "feishu:chatB"

	chA := make(chan struct{}, 1)
	chB := make(chan struct{}, 1)
	cancelMap.Store(chatA, chA)
	cancelMap.Store(chatB, chB)

	// Cancel chatA's task
	if ch, ok := cancelMap.Load(chatA); ok {
		ch.(chan struct{}) <- struct{}{}
	}

	// chatB's channel should still be empty
	select {
	case <-chB:
		t.Error("chatB's cancel channel should not have been signaled")
	default:
		// OK
	}

	// chatA's channel should have the signal
	select {
	case <-chA:
		// OK
	default:
		t.Error("chatA's cancel channel should have been signaled")
	}
}

// TestRun_CancelPreservesEngineMessages verifies that when a Run is cancelled
// after completing some iterations, EngineMessages contains the partial progress.
func TestRun_CancelPreservesEngineMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				Content:      "Let me check something...",
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{"command":"echo hello"}`},
				},
			},
			// Second Generate will be cancelled
		},
	}

	callCount := 0
	out := Run(ctx, RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		ToolExecutor: func(execCtx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			callCount++
			if callCount == 1 {
				time.Sleep(50 * time.Millisecond)
				cancel()
			}
			return tools.NewResult("tool output"), nil
		},
	})

	if out.Error == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(out.Error, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", out.Error)
	}

	if len(out.EngineMessages) == 0 {
		t.Fatal("expected non-empty EngineMessages after cancel, got empty")
	}

	foundAssistant := false
	foundToolResult := false
	for _, em := range out.EngineMessages {
		if em.Role == "assistant" && len(em.ToolCalls) > 0 {
			foundAssistant = true
			if em.ToolCalls[0].Name != "Shell" {
				t.Errorf("expected Shell tool call, got %s", em.ToolCalls[0].Name)
			}
		}
		if em.Role == "tool" {
			foundToolResult = true
		}
	}
	if !foundAssistant {
		t.Error("expected assistant message with tool_calls in EngineMessages")
	}
	if !foundToolResult {
		t.Error("expected tool result message in EngineMessages")
	}

	t.Logf("Cancel preserved %d engine messages (expected >= 2)", len(out.EngineMessages))
}
