package llm

import (
	"context"
	"testing"
	"time"
)

func TestCollectStream_ContentOnly(t *testing.T) {
	ch := make(chan StreamEvent, 10)
	ch <- StreamEvent{Type: EventContent, Content: "hello "}
	ch <- StreamEvent{Type: EventContent, Content: "world"}
	ch <- StreamEvent{Type: EventUsage, Usage: &TokenUsage{PromptTokens: 10, CompletionTokens: 5}}
	ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonStop}
	close(ch)

	resp, err := CollectStream(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello world" {
		t.Errorf("content = %q, want %q", resp.Content, "hello world")
	}
	if resp.FinishReason != FinishReasonStop {
		t.Errorf("finish_reason = %q, want %q", resp.FinishReason, FinishReasonStop)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("usage.prompt = %d, want 10", resp.Usage.PromptTokens)
	}
}

func TestCollectStream_WithToolCalls(t *testing.T) {
	ch := make(chan StreamEvent, 10)
	ch <- StreamEvent{Type: EventContent, Content: "let me "}
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 0, ID: "call_1", Name: "Read", Arguments: `{"path":"`}}
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 0, Arguments: `test.go"}`}}
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 1, ID: "call_2", Name: "Shell", Arguments: `{"command":"ls"`}}
	ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{Index: 1, Arguments: `}`}}
	ch <- StreamEvent{Type: EventContent, Content: "check files"}
	ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonToolCalls}
	close(ch)

	resp, err := CollectStream(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "let me check files" {
		t.Errorf("content = %q, want %q", resp.Content, "let me check files")
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool_calls count = %d, want 2", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "Read" || resp.ToolCalls[0].Arguments != `{"path":"test.go"}` {
		t.Errorf("tool_call[0] = %+v, unexpected", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].ID != "call_2" || resp.ToolCalls[1].Name != "Shell" || resp.ToolCalls[1].Arguments != `{"command":"ls"}` {
		t.Errorf("tool_call[1] = %+v, unexpected", resp.ToolCalls[1])
	}
}

func TestCollectStream_WithReasoning(t *testing.T) {
	ch := make(chan StreamEvent, 10)
	ch <- StreamEvent{Type: EventReasoningContent, ReasoningContent: "think"}
	ch <- StreamEvent{Type: EventReasoningContent, ReasoningContent: " more"}
	ch <- StreamEvent{Type: EventContent, Content: "answer"}
	ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonStop}
	close(ch)

	resp, err := CollectStream(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ReasoningContent != "think more" {
		t.Errorf("reasoning = %q, want %q", resp.ReasoningContent, "think more")
	}
	if resp.Content != "answer" {
		t.Errorf("content = %q, want %q", resp.Content, "answer")
	}
}

func TestCollectStream_Error(t *testing.T) {
	ch := make(chan StreamEvent, 10)
	ch <- StreamEvent{Type: EventContent, Content: "partial"}
	ch <- StreamEvent{Type: EventError, Error: "connection reset"}
	close(ch)

	resp, err := CollectStream(context.Background(), ch)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "stream error: connection reset" {
		t.Errorf("error = %q, want %q", err.Error(), "stream error: connection reset")
	}
	// Verify partial content is preserved in response
	if resp == nil {
		t.Fatal("expected non-nil response with partial content")
	}
	if resp.Content != "partial" {
		t.Errorf("resp.Content = %q, want %q", resp.Content, "partial")
	}
}

func TestCollectStream_ContextCancelled(t *testing.T) {
	ch := make(chan StreamEvent, 10)
	ctx, cancel := context.WithCancel(context.Background())

	// Send one event then cancel
	ch <- StreamEvent{Type: EventContent, Content: "hello"}
	cancel()

	_, err := CollectStream(ctx, ch)
	if err != context.Canceled {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestCollectStream_EmptyChannel(t *testing.T) {
	ch := make(chan StreamEvent, 1)
	close(ch)

	resp, err := CollectStream(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("content = %q, want empty", resp.Content)
	}
}

func TestCollectStream_SlowStream(t *testing.T) {
	// Test that CollectStream correctly processes a stream with delays
	ch := make(chan StreamEvent, 10)
	go func() {
		ch <- StreamEvent{Type: EventContent, Content: "a"}
		time.Sleep(10 * time.Millisecond)
		ch <- StreamEvent{Type: EventContent, Content: "b"}
		time.Sleep(10 * time.Millisecond)
		ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonStop}
		close(ch)
	}()

	resp, err := CollectStream(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ab" {
		t.Errorf("content = %q, want %q", resp.Content, "ab")
	}
}
