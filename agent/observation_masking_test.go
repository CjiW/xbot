package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

func TestObservationMaskStore_BasicOperations(t *testing.T) {
	store := NewObservationMaskStore(10)

	// 测试 Mask
	obs, placeholder := store.Mask("Shell", `{"command":"ls -la"}`, "total 128\ndrwxr-xr-x  5 root root 4096 Mar 20 12:00 .\n...", 10)

	if obs.ID == "" {
		t.Fatal("expected non-empty mask ID")
	}
	if len(obs.ID) != 11 || obs.ID[:3] != "mk_" {
		t.Fatalf("unexpected mask ID format: %s", obs.ID)
	}
	if obs.ToolName != "Shell" {
		t.Fatalf("expected tool name Shell, got %s", obs.ToolName)
	}
	if obs.Content != "total 128\ndrwxr-xr-x  5 root root 4096 Mar 20 12:00 .\n..." {
		t.Fatal("content should be preserved")
	}

	// 验证占位符格式
	if len(placeholder) == 0 {
		t.Fatal("placeholder should not be empty")
	}
	if placeholder == obs.Content {
		t.Fatal("placeholder should not equal original content")
	}

	// 测试 Recall
	recalled, err := store.Recall(obs.ID)
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if recalled.Content != obs.Content {
		t.Fatal("recalled content mismatch")
	}

	// 测试 Recall 不存在的 ID
	_, err = store.Recall("mk_nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent ID")
	}
}

func TestObservationMaskStore_MaxSize(t *testing.T) {
	store := NewObservationMaskStore(3)

	// 填满 3 个
	var firstID string
	for i := 0; i < 5; i++ {
		obs, _ := store.Mask("Shell", `{"command":"echo hello"}`, "output", i)
		if i == 0 {
			firstID = obs.ID
		}
	}

	// 应该只剩 3 个（FIFO 淘汰）
	if store.Size() != 3 {
		t.Fatalf("expected size 3, got %d", store.Size())
	}

	// 第一个应该被淘汰
	_, err := store.Recall(firstID)
	if err == nil {
		t.Fatal("expected first entry to be evicted")
	}
}

func TestObservationMaskStore_ListAndClear(t *testing.T) {
	store := NewObservationMaskStore(10)

	store.Mask("Shell", `{"command":"ls"}`, "file1\nfile2", 0)
	store.Mask("Read", `{"path":"/tmp/test.go"}`, "package main", 1)

	entries := store.List()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	store.Clear()
	if store.Size() != 0 {
		t.Fatalf("expected size 0 after clear, got %d", store.Size())
	}
}

func TestObservationMaskStore_MaskedRecallStore(t *testing.T) {
	store := NewObservationMaskStore(10)

	obs, _ := store.Mask("Shell", `{"command":"cat /etc/hosts"}`, "127.0.0.1 localhost", 0)

	// RecallMasked
	toolName, content, err := store.RecallMasked(obs.ID)
	if err != nil {
		t.Fatalf("RecallMasked failed: %v", err)
	}
	if toolName == "" {
		t.Fatal("toolName should not be empty")
	}
	if content != "127.0.0.1 localhost" {
		t.Fatalf("content mismatch: %s", content)
	}

	// ListMasked
	list := store.ListMasked()
	if len(list) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(list))
	}
	if list[0]["id"] != obs.ID {
		t.Fatal("id mismatch")
	}
}

func TestMaskOldToolResults_Basic(t *testing.T) {
	store := NewObservationMaskStore(100)
	messages := []llm.ChatMessage{
		// Group 1 — 应被遮蔽
		{Role: "assistant", Content: "let me check", ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Shell"}}},
		{Role: "tool", Content: "very long output from first shell command " + string(make([]byte, 500)), ToolName: "Shell", ToolCallID: "tc1", ToolArguments: `{"command":"ls"}`},
		// Group 2 — 应被遮蔽
		{Role: "assistant", Content: "now let me read the file", ToolCalls: []llm.ToolCall{{ID: "tc2", Name: "Read"}}},
		{Role: "tool", Content: "very long file content " + string(make([]byte, 500)), ToolName: "Read", ToolCallID: "tc2", ToolArguments: `{"path":"/tmp/a.go"}`},
		// Group 3 — 保留（最近第1个）
		{Role: "assistant", Content: "checking again", ToolCalls: []llm.ToolCall{{ID: "tc3", Name: "Shell"}}},
		{Role: "tool", Content: "recent output", ToolName: "Shell", ToolCallID: "tc3", ToolArguments: `{"command":"pwd"}`},
	}

	// keepGroups=1 应遮蔽前 2 个 group
	result, count, _ := MaskOldToolResults(messages, store, 1)
	if count != 2 {
		t.Fatalf("expected 2 masked, got %d", count)
	}

	// Group 1 tool result 应被替换为占位符
	if result[1].Content == messages[1].Content {
		t.Fatal("group 1 tool result should be masked (replaced with placeholder)")
	}
	if result[1].Role != "tool" {
		t.Fatal("role should still be tool")
	}

	// Group 3 tool result 应保持不变
	if result[5].Content != "recent output" {
		t.Fatalf("group 3 should be preserved, got: %s", result[5].Content)
	}

	// Assistant 消息应保留但 strip think blocks
	if result[0].Role != "assistant" {
		t.Fatal("assistant role should be preserved")
	}
}

func TestMaskOldToolResults_KeepAll(t *testing.T) {
	store := NewObservationMaskStore(100)
	messages := []llm.ChatMessage{
		{Role: "assistant", Content: "checking", ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Shell"}}},
		{Role: "tool", Content: "output", ToolName: "Shell", ToolCallID: "tc1", ToolArguments: `{}`},
	}

	result, count, _ := MaskOldToolResults(messages, store, 3)
	if count != 0 {
		t.Fatalf("expected 0 masked when keepGroups >= total, got %d", count)
	}
	if len(result) != len(messages) {
		t.Fatal("messages length should not change")
	}
}

func TestMaskOldToolResults_EmptyAndNull(t *testing.T) {
	store := NewObservationMaskStore(100)
	messages := []llm.ChatMessage{
		{Role: "assistant", Content: "checking", ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Shell"}}},
		{Role: "tool", Content: "", ToolName: "Shell", ToolCallID: "tc1", ToolArguments: `{}`},
		{Role: "assistant", Content: "again", ToolCalls: []llm.ToolCall{{ID: "tc2", Name: "Read"}}},
		{Role: "tool", Content: "null", ToolName: "Read", ToolCallID: "tc2", ToolArguments: `{}`},
	}

	_, count, _ := MaskOldToolResults(messages, store, 1)
	// 空和 null 内容的 tool result 应该跳过遮蔽
	if count != 0 {
		t.Fatalf("expected 0 masked for empty/null content, got %d", count)
	}
}

func TestMaskOldToolResults_WithThinkBlocks(t *testing.T) {
	store := NewObservationMaskStore(100)
	messages := []llm.ChatMessage{
		// Group 1 — 应被遮蔽
		{Role: "assistant", Content: "I need to think about this.\n<thinking>Reasoning about the approach</thinking>\nlet me check", ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Shell"}}},
		{Role: "tool", Content: "output data that is long enough to exceed the 300 char threshold " + string(make([]byte, 500)), ToolName: "Shell", ToolCallID: "tc1", ToolArguments: `{}`},
		// Group 2 — 保留
		{Role: "assistant", Content: "checking again", ToolCalls: []llm.ToolCall{{ID: "tc2", Name: "Shell"}}},
		{Role: "tool", Content: "recent output", ToolName: "Shell", ToolCallID: "tc2", ToolArguments: `{}`},
	}

	result, count, _ := MaskOldToolResults(messages, store, 1)
	if count != 1 {
		t.Fatalf("expected 1 masked, got %d", count)
	}

	// Think blocks 应被保留（不 strip）
	if result[0].Content != messages[0].Content {
		t.Fatalf("assistant content with think blocks should be preserved, got %q", result[0].Content)
	}
}

func TestMaskOldToolResults_NoToolMessages(t *testing.T) {
	store := NewObservationMaskStore(100)
	messages := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}

	result, count, _ := MaskOldToolResults(messages, store, 3)
	if count != 0 {
		t.Fatalf("expected 0 masked for no-tool messages, got %d", count)
	}
	if len(result) != 2 {
		t.Fatal("should return same length")
	}
}

func TestMaskOldToolResults_AlreadyMaskedSkipped(t *testing.T) {
	store := NewObservationMaskStore(100)
	messages := []llm.ChatMessage{
		// Group 1: already-masked tool result — should NOT be re-masked
		{Role: "assistant", Content: "checking", ToolCalls: []llm.ToolCall{{ID: "tc1", Name: "Shell"}}},
		{Role: "tool", Content: "📂 [masked:mk_old12345] Shell(ls) — 500 chars — 结果已遮蔽", ToolName: "Shell", ToolCallID: "tc1", ToolArguments: `{"command":"ls"}`},
		// Group 2: normal tool result (long enough to be masked)
		{Role: "assistant", Content: "again", ToolCalls: []llm.ToolCall{{ID: "tc2", Name: "Read"}}},
		{Role: "tool", Content: "file content that is long enough to be masked " + string(make([]byte, 500)), ToolName: "Read", ToolCallID: "tc2", ToolArguments: `{"path":"test.go"}`},
		// Group 3 (kept): recent tool result
		{Role: "assistant", Content: "final", ToolCalls: []llm.ToolCall{{ID: "tc3", Name: "Grep"}}},
		{Role: "tool", Content: "grep results", ToolName: "Grep", ToolCallID: "tc3", ToolArguments: `{}`},
	}

	result, count, _ := MaskOldToolResults(messages, store, 1)
	// Only group 2's tool result should be masked; group 1 is already masked and skipped
	if count != 1 {
		t.Fatalf("expected 1 masked (skip already-masked), got %d", count)
	}
	// Group 1's tool result should be unchanged (not re-masked)
	if !strings.HasPrefix(result[1].Content, "📂 [masked:mk_old12345]") {
		t.Errorf("already-masked content was re-masked: %s", result[1].Content)
	}
	// Group 2's tool result should be newly masked
	if !strings.HasPrefix(result[3].Content, "📂 [masked:mk_") {
		t.Errorf("expected group 2 to be masked, got: %s", result[3].Content)
	}
	// Store should have exactly 1 entry (not 2)
	if store.Size() != 1 {
		t.Errorf("expected 1 store entry, got %d", store.Size())
	}
}
