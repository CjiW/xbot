package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/llm"
)

func makeAssistantWithToolCalls(content string, toolCalls ...llm.ToolCall) llm.ChatMessage {
	return llm.ChatMessage{
		Role:      "assistant",
		Content:   content,
		ToolCalls: toolCalls,
	}
}

func makeToolResult(name, callID, args, content string) llm.ChatMessage {
	return llm.ChatMessage{
		Role:          "tool",
		Content:       content,
		ToolName:      name,
		ToolCallID:    callID,
		ToolArguments: args,
	}
}

func TestThinTail_NoGroups(t *testing.T) {
	tail := []llm.ChatMessage{
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi there"),
	}
	result := thinTail(tail, 3, nil)
	if len(result) != len(tail) {
		t.Fatalf("expected %d messages, got %d", len(tail), len(result))
	}
	if result[0].Content != "hello" || result[1].Content != "hi there" {
		t.Fatal("messages should be unchanged when no tool groups exist")
	}
}

func TestThinTail_FewerGroupsThanKeep(t *testing.T) {
	tail := []llm.ChatMessage{
		llm.NewUserMessage("do something"),
		makeAssistantWithToolCalls("let me read", llm.ToolCall{ID: "1", Name: "Read", Arguments: `{"path":"foo.go"}`}),
		makeToolResult("Read", "1", `{"path":"foo.go"}`, "file content here"),
		makeAssistantWithToolCalls("now edit", llm.ToolCall{ID: "2", Name: "Edit", Arguments: `{"path":"foo.go"}`}),
		makeToolResult("Edit", "2", `{"path":"foo.go"}`, "edit done"),
	}
	result := thinTail(tail, 3, nil)
	// 2 groups < keepGroups=3 → nothing should be thinned
	if result[2].Content != "file content here" {
		t.Fatalf("expected untouched content, got %q", result[2].Content)
	}
	if result[4].Content != "edit done" {
		t.Fatalf("expected untouched content, got %q", result[4].Content)
	}
}

func TestThinTail_ThinsOldGroups(t *testing.T) {
	longContent := strings.Repeat("x", 500)
	longArgs := strings.Repeat("a", 400)

	tail := []llm.ChatMessage{
		llm.NewUserMessage("do task"),
		// Group 1 (will be thinned)
		makeAssistantWithToolCalls("<think>long reasoning</think>decided to read", llm.ToolCall{ID: "1", Name: "Read", Arguments: longArgs}),
		makeToolResult("Read", "1", longArgs, longContent),
		// Group 2 (will be thinned)
		makeAssistantWithToolCalls("next step", llm.ToolCall{ID: "2", Name: "Edit", Arguments: longArgs}),
		makeToolResult("Edit", "2", longArgs, longContent),
		// Group 3 (kept)
		makeAssistantWithToolCalls("step 3", llm.ToolCall{ID: "3", Name: "Shell", Arguments: longArgs}),
		makeToolResult("Shell", "3", longArgs, longContent),
		// Group 4 (kept)
		makeAssistantWithToolCalls("step 4", llm.ToolCall{ID: "4", Name: "Grep", Arguments: longArgs}),
		makeToolResult("Grep", "4", longArgs, longContent),
		// Group 5 (kept)
		makeAssistantWithToolCalls("step 5", llm.ToolCall{ID: "5", Name: "Read", Arguments: longArgs}),
		makeToolResult("Read", "5", longArgs, longContent),
	}

	result := thinTail(tail, 3, nil)

	// User message untouched
	if result[0].Content != "do task" {
		t.Fatalf("user message should be untouched, got %q", result[0].Content)
	}

	// Group 1 (index 1,2) should be thinned
	if strings.Contains(result[1].Content, "<think>") {
		t.Fatal("think blocks should be stripped from thinned assistant messages")
	}
	if len([]rune(result[1].Content)) > 320 {
		t.Fatalf("assistant content should be truncated, len=%d", len([]rune(result[1].Content)))
	}
	if len([]rune(result[1].ToolCalls[0].Arguments)) > 220 {
		t.Fatalf("tool call args should be truncated, len=%d", len([]rune(result[1].ToolCalls[0].Arguments)))
	}
	if len([]rune(result[2].Content)) > 320 {
		t.Fatalf("tool content should be truncated, len=%d", len([]rune(result[2].Content)))
	}
	if len([]rune(result[2].ToolArguments)) > 220 {
		t.Fatalf("tool arguments should be truncated, len=%d", len([]rune(result[2].ToolArguments)))
	}

	// Group 2 (index 3,4) should also be thinned
	if len([]rune(result[3].Content)) > 320 {
		t.Fatalf("group 2 assistant content should be truncated, len=%d", len([]rune(result[3].Content)))
	}

	// Groups 3-5 (index 5-10) should be untouched
	// Group 3: assistant(5), tool(6); Group 4: assistant(7), tool(8); Group 5: assistant(9), tool(10)
	if result[5].Content != "step 3" {
		t.Fatalf("kept group 3 assistant should be untouched, got %q", result[5].Content)
	}
	if result[6].Content != longContent {
		t.Fatal("kept group 3 tool content should be untouched")
	}
	if result[9].Content != "step 5" {
		t.Fatalf("kept group 5 assistant should be untouched, got %q", result[9].Content)
	}
	if result[10].Content != longContent {
		t.Fatal("kept group 5 tool content should be untouched")
	}
}

func TestThinTail_MultipleToolResultsPerGroup(t *testing.T) {
	longContent := strings.Repeat("y", 500)

	tail := []llm.ChatMessage{
		// Group 1 (will be thinned) - assistant calls 2 tools
		makeAssistantWithToolCalls("parallel calls",
			llm.ToolCall{ID: "1a", Name: "Read", Arguments: "args1"},
			llm.ToolCall{ID: "1b", Name: "Grep", Arguments: "args2"},
		),
		makeToolResult("Read", "1a", "args1", longContent),
		makeToolResult("Grep", "1b", "args2", longContent),
		// Group 2 (kept)
		makeAssistantWithToolCalls("single call", llm.ToolCall{ID: "2", Name: "Edit", Arguments: "args3"}),
		makeToolResult("Edit", "2", "args3", "ok"),
	}

	result := thinTail(tail, 1, nil)

	// Group 1 should be thinned (both tool results)
	if len([]rune(result[1].Content)) > 320 {
		t.Fatalf("first tool result should be truncated, len=%d", len([]rune(result[1].Content)))
	}
	if len([]rune(result[2].Content)) > 320 {
		t.Fatalf("second tool result should be truncated, len=%d", len([]rune(result[2].Content)))
	}

	// Group 2 should be untouched
	if result[4].Content != "ok" {
		t.Fatalf("kept group tool content should be untouched, got %q", result[4].Content)
	}
}

func TestThinTail_DeepCopy(t *testing.T) {
	longArgs := strings.Repeat("z", 400)
	original := []llm.ChatMessage{
		makeAssistantWithToolCalls("content", llm.ToolCall{ID: "1", Name: "Read", Arguments: longArgs}),
		makeToolResult("Read", "1", longArgs, strings.Repeat("w", 500)),
		// Kept group
		makeAssistantWithToolCalls("kept", llm.ToolCall{ID: "2", Name: "Edit", Arguments: "short"}),
		makeToolResult("Edit", "2", "short", "done"),
	}

	origArgsLen := len(original[0].ToolCalls[0].Arguments)
	origContentLen := len(original[1].Content)

	_ = thinTail(original, 1, nil)

	// Original messages should not be modified
	if len(original[0].ToolCalls[0].Arguments) != origArgsLen {
		t.Fatal("original ToolCall arguments were mutated")
	}
	if len(original[1].Content) != origContentLen {
		t.Fatal("original tool content was mutated")
	}
}
func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"hello world", 5, "hello...[truncated]"},
		{"你好世界测试", 3, "你好世...[truncated]"},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := truncateRunes(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestCompressMessagesWithFingerprint_InjectsFingerprint(t *testing.T) {
	// 构造含关键信息的消息
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are an assistant."),
		llm.NewUserMessage("Read /workspace/xbot/agent/compress.go and fix the bug"),
		llm.NewAssistantMessage("I'll read the compressMessagesWithFingerprint function."),
		llm.NewUserMessage("Also check /workspace/xbot/agent/engine.go"),
		llm.NewAssistantMessage("Found error: nil pointer dereference in handleCompress. Decided to use a singleton pattern."),
	}

	fp := KeyInfoFingerprint{
		FilePaths:   []string{"/workspace/xbot/agent/compress.go", "/workspace/xbot/agent/engine.go"},
		Identifiers: []string{"compressMessagesWithFingerprint", "handleCompress"},
		Errors:      []string{"nil pointer dereference in handleCompress"},
		Decisions:   []string{"use a singleton pattern"},
	}

	var capturedPrompt string
	mockLLM := &llm.MockLLM{
		GenerateFn: func(ctx context.Context, model string, msgs []llm.ChatMessage, tools []llm.ToolDefinition, thinkingMode string) (*llm.LLMResponse, error) {
			for _, m := range msgs {
				if m.Role == "user" && strings.Contains(m.Content, "CRITICAL") {
					capturedPrompt = m.Content
				}
			}
			return &llm.LLMResponse{Content: "Compressed summary with @file:/workspace/xbot/agent/compress.go @error:nil pointer @decision:singleton"}, nil
		},
	}

	_, err := compressMessagesWithFingerprint(context.Background(), messages, fp, mockLLM, "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedPrompt == "" {
		t.Fatal("expected fingerprint to be injected into prompt, but no CRITICAL section found")
	}
	if !strings.Contains(capturedPrompt, "Must-Preserve Key Information") {
		t.Error("expected 'Must-Preserve Key Information' in prompt")
	}
	if !strings.Contains(capturedPrompt, "/workspace/xbot/agent/compress.go") {
		t.Error("expected file path from fingerprint in prompt")
	}
	if !strings.Contains(capturedPrompt, "nil pointer dereference") {
		t.Error("expected error from fingerprint in prompt")
	}
	if !strings.Contains(capturedPrompt, "singleton pattern") {
		t.Error("expected decision from fingerprint in prompt")
	}
	if !strings.Contains(capturedPrompt, "compressMessagesWithFingerprint") {
		t.Error("expected identifier from fingerprint in prompt")
	}
}

func TestCompressMessagesWithFingerprint_EmptyFingerprintNoInjection(t *testing.T) {
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are an assistant."),
		llm.NewUserMessage("Hello"),
		llm.NewAssistantMessage("Hi there"),
	}

	var capturedPrompt string
	mockLLM := &llm.MockLLM{
		GenerateFn: func(ctx context.Context, model string, msgs []llm.ChatMessage, tools []llm.ToolDefinition, thinkingMode string) (*llm.LLMResponse, error) {
			for _, m := range msgs {
				if m.Role == "user" {
					capturedPrompt = m.Content
				}
			}
			return &llm.LLMResponse{Content: "Summary"}, nil
		},
	}

	_, err := compressMessagesWithFingerprint(context.Background(), messages, KeyInfoFingerprint{}, mockLLM, "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(capturedPrompt, "Must-Preserve Key Information") {
		t.Error("expected NO fingerprint injection when fp is empty")
	}
}
