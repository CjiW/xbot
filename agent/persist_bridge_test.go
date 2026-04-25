package agent

import (
	"testing"

	"xbot/llm"
)

func TestNewPersistenceBridge(t *testing.T) {
	b := NewPersistenceBridge(nil, 5)
	if b.session != nil {
		t.Error("expected nil session")
	}
	if b.LastPersistedCount() != 5 {
		t.Errorf("expected lastPersistedCount=5, got %d", b.LastPersistedCount())
	}

	b2 := NewPersistenceBridge(nil, 0)
	if b2.LastPersistedCount() != 0 {
		t.Errorf("expected lastPersistedCount=0, got %d", b2.LastPersistedCount())
	}
}

func TestIncrementalPersist_NilSession(t *testing.T) {
	b := NewPersistenceBridge(nil, 2)

	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	// nil session → returns nil, count unchanged
	err := b.IncrementalPersist(messages)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if b.LastPersistedCount() != 2 {
		t.Errorf("expected count unchanged at 2, got %d", b.LastPersistedCount())
	}
}

func TestIncrementalPersist_NilSession_MessagesBelowCount(t *testing.T) {
	b := NewPersistenceBridge(nil, 10)

	messages := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	err := b.IncrementalPersist(messages)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if b.LastPersistedCount() != 10 {
		t.Errorf("expected count unchanged at 10, got %d", b.LastPersistedCount())
	}
}

func TestIncrementalPersist_NilSession_EmptyMessages(t *testing.T) {
	b := NewPersistenceBridge(nil, 0)

	err := b.IncrementalPersist(nil)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if b.LastPersistedCount() != 0 {
		t.Errorf("expected count unchanged at 0, got %d", b.LastPersistedCount())
	}
}

func TestMarkAllPersisted(t *testing.T) {
	b := NewPersistenceBridge(nil, 0)

	b.MarkAllPersisted(7)
	if b.LastPersistedCount() != 7 {
		t.Errorf("expected lastPersistedCount=7, got %d", b.LastPersistedCount())
	}

	b.MarkAllPersisted(0)
	if b.LastPersistedCount() != 0 {
		t.Errorf("expected lastPersistedCount=0, got %d", b.LastPersistedCount())
	}

	b.MarkAllPersisted(100)
	if b.LastPersistedCount() != 100 {
		t.Errorf("expected lastPersistedCount=100, got %d", b.LastPersistedCount())
	}
}

func TestLastPersistedCount(t *testing.T) {
	b := NewPersistenceBridge(nil, 42)
	if got := b.LastPersistedCount(); got != 42 {
		t.Errorf("expected 42, got %d", got)
	}
}

func TestComputeEngineMessages_NoNewMessages(t *testing.T) {
	b := NewPersistenceBridge(nil, 3)

	messages := []llm.ChatMessage{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
	}

	result := b.ComputeEngineMessages(messages)
	if result != nil {
		t.Errorf("expected nil for no new messages, got %+v", result)
	}
}

func TestComputeEngineMessages_EmptySlice(t *testing.T) {
	b := NewPersistenceBridge(nil, 0)
	result := b.ComputeEngineMessages(nil)
	if result != nil {
		t.Errorf("expected nil for empty messages, got %+v", result)
	}
}

func TestComputeEngineMessages_CountGreaterThanLen(t *testing.T) {
	b := NewPersistenceBridge(nil, 10)
	messages := []llm.ChatMessage{
		{Role: "user", Content: "a"},
	}
	result := b.ComputeEngineMessages(messages)
	if result != nil {
		t.Errorf("expected nil when count > len, got %+v", result)
	}
}

func TestComputeEngineMessages_HasNewMessages(t *testing.T) {
	b := NewPersistenceBridge(nil, 2)

	messages := []llm.ChatMessage{
		{Role: "user", Content: "old1"},
		{Role: "assistant", Content: "old2"},
		{Role: "user", Content: "new1"},
		{Role: "assistant", Content: "new2"},
	}

	result := b.ComputeEngineMessages(messages)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 engine messages, got %d", len(result))
	}
	if result[0].Content != "new1" {
		t.Errorf("expected first engine msg 'new1', got %q", result[0].Content)
	}
	if result[1].Content != "new2" {
		t.Errorf("expected second engine msg 'new2', got %q", result[1].Content)
	}

	// Verify it's a copy — modifying result should not affect original
	result[0].Content = "mutated"
	if messages[2].Content == "mutated" {
		t.Error("ComputeEngineMessages should return a copy, not a slice into the original")
	}
}

func TestComputeEngineMessages_AllNew(t *testing.T) {
	b := NewPersistenceBridge(nil, 0)

	messages := []llm.ChatMessage{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
	}

	result := b.ComputeEngineMessages(messages)
	if len(result) != 3 {
		t.Fatalf("expected 3 engine messages, got %d", len(result))
	}
}

func TestIsPersisted(t *testing.T) {
	b := NewPersistenceBridge(nil, 5)

	tests := []struct {
		index    int
		expected bool
	}{
		{0, true},
		{1, true},
		{4, true},
		{5, false},
		{6, false},
		{100, false},
	}
	for _, tt := range tests {
		got := b.IsPersisted(tt.index)
		if got != tt.expected {
			t.Errorf("IsPersisted(%d) = %v, want %v", tt.index, got, tt.expected)
		}
	}
}

func TestIsPersisted_ZeroCount(t *testing.T) {
	b := NewPersistenceBridge(nil, 0)
	if b.IsPersisted(0) {
		t.Error("IsPersisted(0) should be false when count is 0")
	}
}

func TestRewriteAfterCompress_NilSession(t *testing.T) {
	b := NewPersistenceBridge(nil, 3)

	sessionView := []llm.ChatMessage{
		{Role: "user", Content: "compressed summary"},
	}

	ok, err := b.RewriteAfterCompress(sessionView, 10)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if !ok {
		t.Error("expected ok=true for nil session")
	}
	// Count should NOT be updated when session is nil (no-op)
	if b.LastPersistedCount() != 3 {
		t.Errorf("expected count unchanged at 3, got %d", b.LastPersistedCount())
	}
}

func TestRewriteAfterCompress_NilSession_EmptyView(t *testing.T) {
	b := NewPersistenceBridge(nil, 5)

	ok, err := b.RewriteAfterCompress(nil, 10)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if !ok {
		t.Error("expected ok=true for nil session with empty view")
	}
	// Count must NOT be updated when session is nil
	if b.LastPersistedCount() != 5 {
		t.Errorf("expected count unchanged at 5, got %d", b.LastPersistedCount())
	}
}

func TestRewriteAfterCompress_NilSession_WithMixedMessages(t *testing.T) {
	b := NewPersistenceBridge(nil, 2)

	sessionView := []llm.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "compressed summary"},
		{Role: "assistant", Content: "response"},
	}

	ok, err := b.RewriteAfterCompress(sessionView, 8)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if !ok {
		t.Error("expected ok=true for nil session with mixed messages")
	}
	// Count must NOT be updated — nil session means no persistence happened
	if b.LastPersistedCount() != 2 {
		t.Errorf("expected count unchanged at 2, got %d", b.LastPersistedCount())
	}
}

func TestRewriteAfterCompress_NilSession_ZeroMsgCount(t *testing.T) {
	b := NewPersistenceBridge(nil, 0)

	sessionView := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
	}

	ok, err := b.RewriteAfterCompress(sessionView, 0)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if !ok {
		t.Error("expected ok=true for nil session")
	}
	if b.LastPersistedCount() != 0 {
		t.Errorf("expected count unchanged at 0, got %d", b.LastPersistedCount())
	}
}

func TestRewriteAfterCompress_NilSession_LargeMsgCount(t *testing.T) {
	b := NewPersistenceBridge(nil, 100)

	sessionView := []llm.ChatMessage{
		{Role: "user", Content: "summary"},
	}

	ok, err := b.RewriteAfterCompress(sessionView, 500)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if !ok {
		t.Error("expected ok=true for nil session")
	}
	// Count should NOT jump to 500 — nil session means nothing was written
	if b.LastPersistedCount() != 100 {
		t.Errorf("expected count unchanged at 100, got %d", b.LastPersistedCount())
	}
}

func TestAssertNoSystemPersist(t *testing.T) {
	t.Run("system message returns error", func(t *testing.T) {
		msg := llm.ChatMessage{Role: "system", Content: "you are a bot"}
		err := assertNoSystemPersist(msg)
		if err == nil {
			t.Error("expected error for system message, got nil")
		}
	})

	t.Run("user message returns nil", func(t *testing.T) {
		msg := llm.ChatMessage{Role: "user", Content: "hello"}
		err := assertNoSystemPersist(msg)
		if err != nil {
			t.Errorf("expected nil for user message, got %v", err)
		}
	})

	t.Run("assistant message returns nil", func(t *testing.T) {
		msg := llm.ChatMessage{Role: "assistant", Content: "hi"}
		err := assertNoSystemPersist(msg)
		if err != nil {
			t.Errorf("expected nil for assistant message, got %v", err)
		}
	})

	t.Run("tool message returns nil", func(t *testing.T) {
		msg := llm.ChatMessage{Role: "tool", Content: "result"}
		err := assertNoSystemPersist(msg)
		if err != nil {
			t.Errorf("expected nil for tool message, got %v", err)
		}
	})
}
