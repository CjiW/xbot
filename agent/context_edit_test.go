package agent

import (
	"testing"

	"xbot/llm"
)

func makeTestMessages() []llm.ChatMessage {
	return []llm.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Please read file A"},
		{Role: "assistant", Content: "I'll read file A for you."},
		{Role: "tool", ToolName: "Read", ToolCallID: "call_1", Content: "This is a very long file content that we want to truncate or delete because it's not needed anymore and takes up too much context space"},
		{Role: "assistant", Content: "Now let me search for something."},
		{Role: "tool", ToolName: "Grep", ToolCallID: "call_2", Content: "Found 50 matches in 30 files with lots of details about each match including line numbers and context"},
		{Role: "assistant", Content: "Based on the results, here's my analysis."},
		{Role: "user", Content: "What about file B?"},
		{Role: "assistant", Content: "Let me read file B."},
	}
}

func TestCountUserVisible(t *testing.T) {
	msgs := makeTestMessages()
	count := countUserVisible(msgs)
	// 9 total - 1 system = 8 visible
	if count != 8 {
		t.Errorf("expected 8 visible messages, got %d", count)
	}
}

func TestUserVisibleIndex(t *testing.T) {
	msgs := makeTestMessages()

	tests := []struct {
		visibleIdx int
		expected   int
	}{
		{0, 1}, // user (Please read file A)
		{1, 2}, // assistant (I'll read file A)
		{2, 3}, // tool (Read)
		{3, 4}, // assistant (Now let me search)
		{4, 5}, // tool (Grep)
		{5, 6}, // assistant (Based on the results)
		{6, 7}, // user (What about file B?)
		{7, 8}, // assistant (Let me read file B)
	}

	for _, tc := range tests {
		got := userVisibleIndex(msgs, tc.visibleIdx)
		if got != tc.expected {
			t.Errorf("userVisibleIndex(%d) = %d, want %d", tc.visibleIdx, got, tc.expected)
		}
	}
}

func TestListMessages(t *testing.T) {
	msgs := makeTestMessages()
	result := listMessagesByTurn(msgs)
	if result == "" {
		t.Error("listMessagesByTurn returned empty string")
	}
	// Should contain turn-based header
	if !containsStr(result, "Conversation Turns") {
		t.Error("listMessagesByTurn should contain 'Conversation Turns' header")
	}
	// Should contain "Turn 0" and "Turn 1" (two user messages)
	if !containsStr(result, "Turn 0") {
		t.Error("listMessagesByTurn should contain 'Turn 0'")
	}
	if !containsStr(result, "Turn 1") {
		t.Error("listMessagesByTurn should contain 'Turn 1'")
	}
}

func TestContextEditor_List(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	editor.SetMessages(makeTestMessages())

	result, err := editor.HandleRequest("list", nil)
	if err != nil {
		t.Fatalf("HandleRequest(list) error: %v", err)
	}
	if result == "" {
		t.Error("list returned empty result")
	}
}

func TestContextEditor_Delete(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	// Delete message index 3 (Grep tool result)
	result, err := editor.HandleRequest("delete", map[string]any{
		"message_idx": float64(3),
		"reason":      "outdated search results",
	})
	if err != nil {
		t.Fatalf("HandleRequest(delete) error: %v", err)
	}
	if result == "" {
		t.Error("delete returned empty result")
	}

	// Verify the message content was replaced
	actualIdx := userVisibleIndex(msgs, 3)
	if actualIdx < 0 || !containsStr(msgs[actualIdx].Content, "[context edited:") {
		t.Errorf("message content was not replaced, got: %s", msgs[actualIdx].Content)
	}

	// Tool calls should be cleared
	if len(msgs[actualIdx].ToolCalls) > 0 {
		t.Error("ToolCalls should be nil after delete")
	}

	// Verify history
	history := store.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(history))
	}
	if history[0].Action != ContextEditDelete {
		t.Errorf("expected delete action, got %s", history[0].Action)
	}
}

func TestContextEditor_Truncate(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	// Truncate message index 2 (Read tool result) to 20 chars
	result, err := editor.HandleRequest("truncate", map[string]any{
		"message_idx": float64(2),
		"max_chars":   float64(20),
		"reason":      "file content no longer needed",
	})
	if err != nil {
		t.Fatalf("HandleRequest(truncate) error: %v", err)
	}
	if result == "" {
		t.Error("truncate returned empty result")
	}

	// Verify truncation
	actualIdx := userVisibleIndex(msgs, 2)
	content := msgs[actualIdx].Content
	runes := []rune(content)
	// Should have the first 20 chars + edit marker
	if !containsStr(content, "truncated from") {
		t.Errorf("expected truncation marker in content, got: %s", content[:min(100, len(content))])
	}
	if !containsStr(content, "[context edited:") {
		t.Errorf("expected context edit marker in content, got: %s", content[:min(100, len(content))])
	}
	_ = runes
}

func TestContextEditor_Replace(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	// Replace text in message index 4 (Grep tool result)
	result, err := editor.HandleRequest("replace", map[string]any{
		"message_idx": float64(4),
		"old_text":    "Found 50 matches",
		"new_text":    "Found matches (details removed)",
		"reason":      "reduce grep output verbosity",
	})
	if err != nil {
		t.Fatalf("HandleRequest(replace) error: %v", err)
	}

	// Verify replacement
	actualIdx := userVisibleIndex(msgs, 4)
	content := msgs[actualIdx].Content
	if !containsStr(content, "Found matches (details removed)") {
		t.Errorf("replacement not applied, got: %s", content[:min(200, len(content))])
	}
	if containsStr(content, "Found 50 matches in 30 files") {
		t.Error("old text should have been replaced")
	}
	_ = result
}

func TestContextEditor_ReplaceRegex(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	// Regex replace
	result, err := editor.HandleRequest("replace", map[string]any{
		"message_idx": float64(4),
		"old_text":    "regex:\\d+ matches",
		"new_text":    "N matches",
		"reason":      "redact specific numbers",
	})
	if err != nil {
		t.Fatalf("HandleRequest(regex replace) error: %v", err)
	}
	_ = result
}

func TestContextEditor_ReplaceRegexInvalid(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	// Invalid regex should error
	_, err := editor.HandleRequest("replace", map[string]any{
		"message_idx": float64(4),
		"old_text":    "regex:[invalid",
		"new_text":    "N",
		"reason":      "test invalid regex",
	})
	if err == nil {
		t.Error("expected error for invalid regex pattern")
	}
}

func TestContextEditor_ReplaceNotFound(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	_, err := editor.HandleRequest("replace", map[string]any{
		"message_idx": float64(4),
		"old_text":    "this text does not exist",
		"new_text":    "replacement",
		"reason":      "test not found",
	})
	if err == nil {
		t.Error("expected error when old_text not found")
	}
}

func TestContextEditor_SafetyChecks(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	// Test: cannot delete last 3 messages
	_, err := editor.HandleRequest("delete", map[string]any{
		"message_idx": float64(7), // last message
		"reason":      "should fail",
	})
	if err == nil {
		t.Error("expected error: cannot edit last 3 messages")
	}
	if !containsStr(err.Error(), "protected") {
		t.Errorf("expected 'protected' in error, got: %s", err.Error())
	}

	// Test: out of range index
	_, err = editor.HandleRequest("delete", map[string]any{
		"message_idx": float64(100),
		"reason":      "out of range",
	})
	if err == nil {
		t.Error("expected error: out of range")
	}

	// Test: unknown action
	_, err = editor.HandleRequest("explode", map[string]any{})
	if err == nil {
		t.Error("expected error: unknown action")
	}
}

func TestContextEditor_TruncateAlreadySmall(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	// Try to truncate a short message to a large number
	_, err := editor.HandleRequest("truncate", map[string]any{
		"message_idx": float64(0),
		"max_chars":   float64(10000),
		"reason":      "already small",
	})
	if err == nil {
		t.Error("expected error: already within limit")
	}
}

func TestContextEditor_MissingMessageIdx(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	editor.SetMessages(makeTestMessages())

	_, err := editor.HandleRequest("delete", map[string]any{
		"reason": "missing idx",
	})
	if err == nil {
		t.Error("expected error: message_idx is required")
	}
}

func TestContextEditor_ReplaceMissingOldText(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	editor.SetMessages(makeTestMessages())

	_, err := editor.HandleRequest("replace", map[string]any{
		"message_idx": float64(2),
		"reason":      "missing old_text",
	})
	if err == nil {
		t.Error("expected error: old_text is required for replace")
	}
}

func TestContextEditStore_MaxSize(t *testing.T) {
	store := NewContextEditStore(5)

	for i := 0; i < 10; i++ {
		store.Record(ContextEditResult{
			Action:     ContextEditDelete,
			MessageIdx: i,
			Role:       "tool",
			Reason:     "test",
		})
	}

	history := store.History()
	if len(history) != 5 {
		t.Errorf("expected 5 entries (maxSize), got %d", len(history))
	}
	// Most recent first, so last recorded should be first in history
	if history[0].MessageIdx != 9 {
		t.Errorf("expected first history entry to have idx 9, got %d", history[0].MessageIdx)
	}
}

func TestContextEditor_NoMessagesSet(t *testing.T) {
	editor := NewContextEditor(nil)
	// No messages set
	_, err := editor.HandleRequest("list", nil)
	if err == nil {
		t.Error("expected error: messages not available")
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestIdentifyTurns(t *testing.T) {
	msgs := makeTestMessages()
	turns := identifyTurns(msgs)

	// Should have 2 turns (2 user messages)
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}

	// Turn 0: user(1) + assistant(2) + tool(3) + assistant(4) + tool(5) + assistant(6) = 6 messages
	t0 := turns[0]
	if t0.TurnIdx != 0 {
		t.Errorf("Turn 0: expected TurnIdx=0, got %d", t0.TurnIdx)
	}
	if t0.StartSliceIdx != 1 {
		t.Errorf("Turn 0: expected StartSliceIdx=1, got %d", t0.StartSliceIdx)
	}
	if t0.EndSliceIdx != 6 {
		t.Errorf("Turn 0: expected EndSliceIdx=6, got %d", t0.EndSliceIdx)
	}
	if t0.MsgCount != 6 {
		t.Errorf("Turn 0: expected MsgCount=6, got %d", t0.MsgCount)
	}
	if t0.ToolCount != 2 {
		t.Errorf("Turn 0: expected ToolCount=2, got %d", t0.ToolCount)
	}

	// Turn 1: user(7) + assistant(8) = 2 messages
	t1 := turns[1]
	if t1.TurnIdx != 1 {
		t.Errorf("Turn 1: expected TurnIdx=1, got %d", t1.TurnIdx)
	}
	if t1.StartSliceIdx != 7 {
		t.Errorf("Turn 1: expected StartSliceIdx=7, got %d", t1.StartSliceIdx)
	}
	if t1.EndSliceIdx != 8 {
		t.Errorf("Turn 1: expected EndSliceIdx=8, got %d", t1.EndSliceIdx)
	}
	if t1.MsgCount != 2 {
		t.Errorf("Turn 1: expected MsgCount=2, got %d", t1.MsgCount)
	}
	if t1.ToolCount != 0 {
		t.Errorf("Turn 1: expected ToolCount=0, got %d", t1.ToolCount)
	}
}

func TestIdentifyTurns_NoUserMessages(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "system prompt"},
	}
	turns := identifyTurns(msgs)
	if len(turns) != 0 {
		t.Errorf("expected 0 turns for system-only messages, got %d", len(turns))
	}
}

func TestContextEditor_DeleteTurn(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	// Delete turn 0 (user msg at idx 0 through assistant at idx 6 in visible, slice 1-6)
	result, err := editor.HandleRequest("delete_turn", map[string]any{
		"turn_idx": float64(0),
		"reason":   "old conversation",
	})
	if err != nil {
		t.Fatalf("HandleRequest(delete_turn) error: %v", err)
	}
	if !containsStr(result, "Deleted turn 0") {
		t.Errorf("expected 'Deleted turn 0' in result, got: %s", result)
	}

	// Verify all messages in turn 0 are replaced
	for i := 1; i <= 6; i++ {
		if !containsStr(msgs[i].Content, "[context edited:") {
			t.Errorf("message slice idx %d (role=%s) should be replaced, got: %s", i, msgs[i].Role, msgs[i].Content[:min(80, len(msgs[i].Content))])
		}
		// ToolCalls should be cleared for assistant messages
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			t.Errorf("message slice idx %d: ToolCalls should be nil", i)
		}
	}

	// Verify turn 1 (last turn, protected) is NOT modified
	if containsStr(msgs[7].Content, "[context edited:") {
		t.Error("turn 1 user message should not be modified")
	}
	if containsStr(msgs[8].Content, "[context edited:") {
		t.Error("turn 1 assistant message should not be modified")
	}

	// Verify history
	history := store.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(history))
	}
	if history[0].Action != ContextEditDeleteTurn {
		t.Errorf("expected delete_turn action, got %s", history[0].Action)
	}
}

func TestContextEditor_DeleteTurnLastProtected(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	// Try to delete last turn (turn 1) — should fail
	_, err := editor.HandleRequest("delete_turn", map[string]any{
		"turn_idx": float64(1),
		"reason":   "should fail",
	})
	if err == nil {
		t.Error("expected error: cannot delete last turn")
	}
	if !containsStr(err.Error(), "protected") {
		t.Errorf("expected 'protected' in error, got: %s", err.Error())
	}
}

func TestContextEditor_DeleteTurnOutOfRange(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	_, err := editor.HandleRequest("delete_turn", map[string]any{
		"turn_idx": float64(99),
		"reason":   "out of range",
	})
	if err == nil {
		t.Error("expected error: out of range")
	}
}

func TestContextEditor_DeleteTurnMissingIdx(t *testing.T) {
	store := NewContextEditStore(10)
	editor := NewContextEditor(store)
	msgs := makeTestMessages()
	editor.SetMessages(msgs)

	_, err := editor.HandleRequest("delete_turn", map[string]any{
		"reason": "missing turn_idx",
	})
	if err == nil {
		t.Error("expected error: turn_idx is required")
	}
}
