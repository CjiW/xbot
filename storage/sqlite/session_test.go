package sqlite

import (
	"testing"

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
