package agent

import (
	"fmt"
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

// TestThinTail_ActiveFilesNoLongerSkips verifies the BUG FIX:
// activeFiles 保护的组现在做轻量截断（而非完全跳过）。
// 旧逻辑：涉及活跃文件的组被 continue 跳过，编程会话中几乎所有组都涉及活跃文件 → 完全无效。
func TestThinTail_ActiveFilesNoLongerSkips(t *testing.T) {
	// 5 个工具组，每个操作 agent/compress.go（活跃文件）
	// keepGroups=1 → 应截断前 4 组
	activeFiles := []ActiveFile{{Path: "agent/compress.go", LastSeenIter: 0}}

	tail := make([]llm.ChatMessage, 0, 10) // 5 assistant + 5 tool = 10
	for i := 0; i < 5; i++ {
		longContent := strings.Repeat("x", 2000)
		tail = append(tail, makeAssistantWithToolCalls(longContent, llm.ToolCall{
			Name: "Read", Arguments: `{"path":"agent/compress.go"}`,
		}))
		tail = append(tail, llm.NewToolMessage("Read", "", "", fmt.Sprintf("tool result %d", i)))
	}

	result := thinTail(tail, 1, activeFiles)

	// 前 4 组应该被截断（不再完全跳过）
	// 组 0: assistant idx=0, tool idx=1
	// 组 1: assistant idx=2, tool idx=3
	// 组 2: assistant idx=4, tool idx=5
	// 组 3: assistant idx=6, tool idx=7
	// 组 4: 完整保留 (keepGroups=1)
	for i := 0; i < 4; i++ {
		asstIdx := i * 2
		toolIdx := i*2 + 1
		// assistant content 应被截断到 activeContentMax (800)
		if len([]rune(result[asstIdx].Content)) > 820 {
			t.Errorf("active group %d: assistant content not truncated, got %d runes", i, len([]rune(result[asstIdx].Content)))
		}
		// tool content 应被截断到 activeContentMax (800)
		if len([]rune(result[toolIdx].Content)) > 820 {
			t.Errorf("active group %d: tool content not truncated, got %d runes", i, len([]rune(result[toolIdx].Content)))
		}
	}

	// 最后 1 组应该完整保留
	lastAsst := result[8]
	if len([]rune(lastAsst.Content)) != 2000 {
		t.Errorf("last kept group assistant: expected 2000 runes, got %d", len([]rune(lastAsst.Content)))
	}
	lastTool := result[9]
	if lastTool.Content != "tool result 4" {
		t.Errorf("last kept group tool: expected full content, got %q", lastTool.Content)
	}
}

// TestAggressiveThinTail_ActiveFilesNoLongerSkips same fix for aggressiveThinTail.
func TestAggressiveThinTail_ActiveFilesNoLongerSkips(t *testing.T) {
	activeFiles := []ActiveFile{{Path: "agent/engine.go", LastSeenIter: 0}}

	tail := make([]llm.ChatMessage, 0, 10)
	for i := 0; i < 5; i++ {
		longContent := strings.Repeat("y", 2000)
		tail = append(tail, makeAssistantWithToolCalls(longContent, llm.ToolCall{
			Name: "Edit", Arguments: `{"path":"agent/engine.go"}`,
		}))
		tail = append(tail, llm.NewToolMessage("Edit", "", "", fmt.Sprintf("result %d", i)))
	}

	result := aggressiveThinTail(tail, 1, activeFiles)

	// 前 4 组应被截断（active 组：200 chars；non-active 组：100 chars）
	for i := 0; i < 4; i++ {
		toolIdx := i*2 + 1
		// active 组 tool content 应截断到 activeContentMax (200)
		if len([]rune(result[toolIdx].Content)) > 210 {
			t.Errorf("active group %d: tool content not truncated, got %d runes", i, len([]rune(result[toolIdx].Content)))
		}
	}

	// 最后 1 组应完整保留
	lastAsst := result[8]
	if len([]rune(lastAsst.Content)) != 2000 {
		t.Errorf("last kept group: expected 2000 runes, got %d", len([]rune(lastAsst.Content)))
	}
}
