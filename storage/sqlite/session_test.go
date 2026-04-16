package sqlite

import (
	"testing"
	"time"

	"xbot/llm"
)

func TestSessionService_AddMessage(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Add messages
	msg1 := llm.NewUserMessage("Hello")
	msg2 := llm.NewAssistantMessage("Hi there")
	msg3 := llm.NewToolMessage("test_tool", "call123", "{}", "Result")

	if err := sessionSvc.AddMessage(tenantID, msg1); err != nil {
		t.Fatalf("Failed to add message 1: %v", err)
	}
	if err := sessionSvc.AddMessage(tenantID, msg2); err != nil {
		t.Fatalf("Failed to add message 2: %v", err)
	}
	if err := sessionSvc.AddMessage(tenantID, msg3); err != nil {
		t.Fatalf("Failed to add message 3: %v", err)
	}

	// Verify count
	count, err := sessionSvc.GetMessagesCount(tenantID)
	if err != nil {
		t.Fatalf("Failed to get messages count: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 messages, got %d", count)
	}
}

func TestSessionService_GetHistory(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Add messages
	for i := 0; i < 10; i++ {
		msg := llm.NewUserMessage("Message " + string(rune('0'+i)))
		if err := sessionSvc.AddMessage(tenantID, msg); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	// Get last 5 messages
	history, err := sessionSvc.GetHistory(tenantID, 5)
	if err != nil {
		t.Fatalf("Failed to get history: %v", err)
	}
	if len(history) != 5 {
		t.Errorf("Expected 5 messages in history, got %d", len(history))
	}

	// Get all messages
	allHistory, err := sessionSvc.GetHistory(tenantID, 100)
	if err != nil {
		t.Fatalf("Failed to get all history: %v", err)
	}
	if len(allHistory) != 10 {
		t.Errorf("Expected 10 messages in all history, got %d", len(allHistory))
	}
}

func TestSessionService_GetAllMessages(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create two tenants
	tenantID1, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant 1: %v", err)
	}
	tenantID2, err := tenantSvc.GetOrCreateTenantID("test", "chat2")
	if err != nil {
		t.Fatalf("Failed to create tenant 2: %v", err)
	}

	// Add messages to tenant 1
	msg1 := llm.NewUserMessage("Tenant 1 message")
	if err := sessionSvc.AddMessage(tenantID1, msg1); err != nil {
		t.Fatalf("Failed to add message to tenant 1: %v", err)
	}

	// Add messages to tenant 2
	msg2 := llm.NewUserMessage("Tenant 2 message")
	if err := sessionSvc.AddMessage(tenantID2, msg2); err != nil {
		t.Fatalf("Failed to add message to tenant 2: %v", err)
	}

	// Get messages for tenant 1
	msgs1, err := sessionSvc.GetAllMessages(tenantID1)
	if err != nil {
		t.Fatalf("Failed to get messages for tenant 1: %v", err)
	}
	if len(msgs1) != 1 {
		t.Errorf("Expected 1 message for tenant 1, got %d", len(msgs1))
	}
	if msgs1[0].Content != "Tenant 1 message" {
		t.Errorf("Expected 'Tenant 1 message', got '%s'", msgs1[0].Content)
	}

	// Get messages for tenant 2
	msgs2, err := sessionSvc.GetAllMessages(tenantID2)
	if err != nil {
		t.Fatalf("Failed to get messages for tenant 2: %v", err)
	}
	if len(msgs2) != 1 {
		t.Errorf("Expected 1 message for tenant 2, got %d", len(msgs2))
	}
	if msgs2[0].Content != "Tenant 2 message" {
		t.Errorf("Expected 'Tenant 2 message', got '%s'", msgs2[0].Content)
	}
}

func TestSessionService_Clear(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Add messages
	for i := 0; i < 5; i++ {
		msg := llm.NewUserMessage("Message")
		if err := sessionSvc.AddMessage(tenantID, msg); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	// Verify count
	count, err := sessionSvc.GetMessagesCount(tenantID)
	if err != nil {
		t.Fatalf("Failed to get messages count: %v", err)
	}
	if count != 5 {
		t.Errorf("Expected 5 messages before clear, got %d", count)
	}

	// Clear messages
	if err := sessionSvc.Clear(tenantID); err != nil {
		t.Fatalf("Failed to clear messages: %v", err)
	}

	// Verify count after clear
	count, err = sessionSvc.GetMessagesCount(tenantID)
	if err != nil {
		t.Fatalf("Failed to get messages count after clear: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 messages after clear, got %d", count)
	}
}

func TestSessionService_ToolCalls(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	// Create tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Add message with tool calls
	msg := llm.ChatMessage{
		Role:    "assistant",
		Content: "Let me help you",
		ToolCalls: []llm.ToolCall{
			{ID: "call1", Name: "tool1", Arguments: "{\"arg\":\"value\"}"},
			{ID: "call2", Name: "tool2", Arguments: "{}"},
		},
	}

	if err := sessionSvc.AddMessage(tenantID, msg); err != nil {
		t.Fatalf("Failed to add message with tool calls: %v", err)
	}

	// Retrieve and verify
	messages, err := sessionSvc.GetAllMessages(tenantID)
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}
	if len(messages[0].ToolCalls) != 2 {
		t.Errorf("Expected 2 tool calls, got %d", len(messages[0].ToolCalls))
	}
	if messages[0].ToolCalls[0].Name != "tool1" {
		t.Errorf("Expected tool name 'tool1', got '%s'", messages[0].ToolCalls[0].Name)
	}
}

func TestSessionService_PurgeNewerThanOrEqual(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Add messages with controlled timestamps (1 second apart to ensure unique created_at)
	baseTime := time.Date(2026, 4, 16, 12, 0, 0, 0, time.Local)
	msgs := []llm.ChatMessage{
		llm.NewUserMessage("U1"),
		llm.NewAssistantMessage("A1"),
		llm.NewUserMessage("U2"),
		llm.NewAssistantMessage("A2"),
		llm.NewUserMessage("U3"),
		llm.NewAssistantMessage("A3"),
		llm.NewUserMessage("U4"),
		llm.NewAssistantMessage("A4"),
	}
	for i, m := range msgs {
		m.Timestamp = baseTime.Add(time.Duration(i) * time.Second)
		if err := sessionSvc.AddMessage(tenantID, m); err != nil {
			t.Fatalf("Failed to add message %d: %v", i, err)
		}
	}

	// Purge from U3 onwards (timestamp = baseTime + 4s)
	cutoff := baseTime.Add(4 * time.Second)
	purged, err := sessionSvc.PurgeNewerThanOrEqual(tenantID, cutoff)
	if err != nil {
		t.Fatalf("PurgeNewerThanOrEqual: %v", err)
	}
	if purged != 4 {
		t.Errorf("expected 4 purged (U3, A3, U4, A4), got %d", purged)
	}

	// Verify remaining messages
	remaining, err := sessionSvc.GetAllMessages(tenantID)
	if err != nil {
		t.Fatalf("GetAllMessages: %v", err)
	}
	if len(remaining) != 4 {
		t.Errorf("expected 4 remaining (U1, A1, U2, A2), got %d", len(remaining))
	}

	// Verify first message content
	if remaining[0].Content != "U1" {
		t.Errorf("first message = %q, want %q", remaining[0].Content, "U1")
	}
}

func TestSessionService_PurgeNewerThanOrEqual_MultiCycle(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)

	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	baseTime := time.Date(2026, 4, 16, 12, 0, 0, 0, time.Local)
	add := func(role, content string, offset int) {
		m := llm.ChatMessage{Role: role, Content: content}
		m.Timestamp = baseTime.Add(time.Duration(offset) * time.Second)
		if err := sessionSvc.AddMessage(tenantID, m); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	// Initial state: U1(0s), A1(1s), U2(2s), A2(3s), U3(4s), A3(5s)
	add("user", "U1", 0)
	add("assistant", "A1", 1)
	add("user", "U2", 2)
	add("assistant", "A2", 3)
	add("user", "U3", 4)
	add("assistant", "A3", 5)

	// Cycle 1: Rewind to U3 (4s) — removes U3 and A3
	sessionSvc.PurgeNewerThanOrEqual(tenantID, baseTime.Add(4*time.Second))
	count, _ := sessionSvc.GetMessagesCount(tenantID)
	if count != 4 {
		t.Errorf("cycle 1: expected 4 messages, got %d", count)
	}

	// Re-send U3 (new timestamp 10s), cancel (simulated by just adding messages)
	add("user", "U3-v2", 10)
	add("assistant", "A3-v2", 11)
	add("tool", "tool_result", 12)

	// Cycle 2: Rewind to U3-v2 (10s) — removes U3-v2 and everything after
	sessionSvc.PurgeNewerThanOrEqual(tenantID, baseTime.Add(10*time.Second))
	count, _ = sessionSvc.GetMessagesCount(tenantID)
	if count != 4 {
		t.Errorf("cycle 2: expected 4 messages (U1,A1,U2,A2), got %d", count)
	}

	// Re-send U3 again (new timestamp 20s), add more messages
	add("user", "U3-v3", 20)
	add("assistant", "A3-v3", 21)
	add("user", "U4", 22)
	add("assistant", "A4", 23)

	// Cycle 3: Rewind to U2 (2s) — removes U2 and everything after
	sessionSvc.PurgeNewerThanOrEqual(tenantID, baseTime.Add(2*time.Second))
	count, _ = sessionSvc.GetMessagesCount(tenantID)
	if count != 2 {
		t.Errorf("cycle 3: expected 2 messages (U1,A1), got %d", count)
	}

	// Verify first message is still U1
	remaining, _ := sessionSvc.GetAllMessages(tenantID)
	if len(remaining) > 0 && remaining[0].Content != "U1" {
		t.Errorf("after cycle 3: first message = %q, want %q", remaining[0].Content, "U1")
	}
}
