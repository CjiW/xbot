package session

import (
	"testing"

	"xbot/llm"
)

func TestMultiTenantSession_GetOrCreateSession(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	// Create session for tenant 1
	sess1, err := mt.GetOrCreateSession("feishu", "chat123")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	if sess1 == nil {
		t.Fatal("Session is nil")
	}
	if sess1.Channel() != "feishu" {
		t.Errorf("Expected channel 'feishu', got '%s'", sess1.Channel())
	}
	if sess1.ChatID() != "chat123" {
		t.Errorf("Expected chatID 'chat123', got '%s'", sess1.ChatID())
	}

	// Get same session - should return cached version
	sess1Again, err := mt.GetOrCreateSession("feishu", "chat123")
	if err != nil {
		t.Fatalf("Failed to get existing session: %v", err)
	}
	if sess1Again.TenantID() != sess1.TenantID() {
		t.Error("Expected same tenant ID for same channel/chat_id")
	}

	// Create session for different tenant
	sess2, err := mt.GetOrCreateSession("feishu", "chat456")
	if err != nil {
		t.Fatalf("Failed to create second session: %v", err)
	}
	if sess2.TenantID() == sess1.TenantID() {
		t.Error("Expected different tenant IDs for different chat IDs")
	}

	// Create session for different channel
	sess3, err := mt.GetOrCreateSession("slack", "chat123")
	if err != nil {
		t.Fatalf("Failed to create session with different channel: %v", err)
	}
	if sess3.TenantID() == sess1.TenantID() || sess3.TenantID() == sess2.TenantID() {
		t.Error("Expected different tenant ID for different channel")
	}
}

func TestMultiTenantSession_Isolation(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	// Create two sessions
	sess1, err := mt.GetOrCreateSession("feishu", "chat1")
	if err != nil {
		t.Fatalf("Failed to create session 1: %v", err)
	}
	sess2, err := mt.GetOrCreateSession("feishu", "chat2")
	if err != nil {
		t.Fatalf("Failed to create session 2: %v", err)
	}

	// Add messages to session 1
	msg1 := llm.NewUserMessage("Session 1 message")
	if err := sess1.AddMessage(msg1); err != nil {
		t.Fatalf("Failed to add message to session 1: %v", err)
	}

	// Add messages to session 2
	msg2 := llm.NewUserMessage("Session 2 message")
	if err := sess2.AddMessage(msg2); err != nil {
		t.Fatalf("Failed to add message to session 2: %v", err)
	}

	// Verify isolation
	history1, err := sess1.GetHistory(10)
	if err != nil {
		t.Fatalf("Failed to get history for session 1: %v", err)
	}
	if len(history1) != 1 {
		t.Errorf("Expected 1 message in session 1, got %d", len(history1))
	}
	if len(history1) > 0 && history1[0].Content != "Session 1 message" {
		t.Errorf("Expected 'Session 1 message', got '%s'", history1[0].Content)
	}

	history2, err := sess2.GetHistory(10)
	if err != nil {
		t.Fatalf("Failed to get history for session 2: %v", err)
	}
	if len(history2) != 1 {
		t.Errorf("Expected 1 message in session 2, got %d", len(history2))
	}
	if len(history2) > 0 && history2[0].Content != "Session 2 message" {
		t.Errorf("Expected 'Session 2 message', got '%s'", history2[0].Content)
	}
}

func TestMultiTenantSession_MemoryIsolation(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	// Create two sessions
	sess1, err := mt.GetOrCreateSession("feishu", "chat1")
	if err != nil {
		t.Fatalf("Failed to create session 1: %v", err)
	}
	sess2, err := mt.GetOrCreateSession("feishu", "chat2")
	if err != nil {
		t.Fatalf("Failed to create session 2: %v", err)
	}

	// Set memory for session 1
	mem1 := sess1.Memory()
	if err := mem1.WriteLongTerm("# Memory 1\nUser likes Go"); err != nil {
		t.Fatalf("Failed to write memory 1: %v", err)
	}

	// Set memory for session 2
	mem2 := sess2.Memory()
	if err := mem2.WriteLongTerm("# Memory 2\nUser likes Rust"); err != nil {
		t.Fatalf("Failed to write memory 2: %v", err)
	}

	// Verify memory isolation
	content1, err := mem1.ReadLongTerm()
	if err != nil {
		t.Fatalf("Failed to read memory 1: %v", err)
	}
	if content1 != "# Memory 1\nUser likes Go" {
		t.Errorf("Memory 1 incorrect: %s", content1)
	}

	content2, err := mem2.ReadLongTerm()
	if err != nil {
		t.Fatalf("Failed to read memory 2: %v", err)
	}
	if content2 != "# Memory 2\nUser likes Rust" {
		t.Errorf("Memory 2 incorrect: %s", content2)
	}
}
