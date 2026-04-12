package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xbot/config"
	"xbot/llm"
	"xbot/memory/letta"
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

	// Write memory directly to MEMORY.md files (flat memory is now file-based)
	// Directory name = tenantID (numeric)
	memDir1 := filepath.Join(config.XbotHome(), "memory", "1")
	memDir2 := filepath.Join(config.XbotHome(), "memory", "2")
	os.MkdirAll(memDir1, 0o755)
	os.MkdirAll(memDir2, 0o755)
	os.WriteFile(filepath.Join(memDir1, "MEMORY.md"), []byte("# Memory 1\nUser likes Go"), 0o644)
	os.WriteFile(filepath.Join(memDir2, "MEMORY.md"), []byte("# Memory 2\nUser likes Rust"), 0o644)

	// Verify memory isolation via Recall
	ctx := context.Background()
	content1, err := sess1.Memory().Recall(ctx, "")
	if err != nil {
		t.Fatalf("Failed to recall memory 1: %v", err)
	}
	if !strings.Contains(content1, "User likes Go") {
		t.Errorf("Memory 1 incorrect: %s", content1)
	}

	content2, err := sess2.Memory().Recall(ctx, "")
	if err != nil {
		t.Fatalf("Failed to recall memory 2: %v", err)
	}
	if !strings.Contains(content2, "User likes Rust") {
		t.Errorf("Memory 2 incorrect: %s", content2)
	}
}

func TestMigrateProfileToCoreMemory_MigratesMe(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath, WithMemoryProvider("letta"))
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	// Insert __me__ profile before creating a session
	selfProfile := "- I am xbot\n- I like Go programming\n- I value clarity"
	if err := mt.userProfileSvc.SaveProfile("__me__", "xbot", selfProfile); err != nil {
		t.Fatalf("Failed to save __me__ profile: %v", err)
	}

	// Create a Letta session — should trigger migration
	sess, err := mt.GetOrCreateSession("feishu", "chat_migrate")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Check that persona block was populated (persona is global, use "" for userID)
	persona, _, err := mt.coreSvc.GetBlock(sess.TenantID(), "persona", "")
	if err != nil {
		t.Fatalf("Failed to read persona block: %v", err)
	}
	if persona != selfProfile {
		t.Errorf("Expected persona block to be '%s', got '%s'", selfProfile, persona)
	}
}

func TestMigrateProfileToCoreMemory_SkipsIfPersonaPopulated(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath, WithMemoryProvider("letta"))
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	// Insert __me__ profile
	if err := mt.userProfileSvc.SaveProfile("__me__", "xbot", "old profile data"); err != nil {
		t.Fatalf("Failed to save __me__ profile: %v", err)
	}

	// Create tenant and pre-populate persona block
	tenantID, err := mt.tenantSvc.GetOrCreateTenantID("feishu", "chat_skip")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}
	if err := mt.coreSvc.InitBlocks(tenantID, ""); err != nil {
		t.Fatalf("Failed to init blocks: %v", err)
	}
	existingPersona := "- Already configured persona"
	if err := mt.coreSvc.SetBlock(tenantID, "persona", existingPersona, ""); err != nil {
		t.Fatalf("Failed to set existing persona: %v", err)
	}

	// Run migration — should NOT overwrite
	mt.migrateProfileToCoreMemory(tenantID)

	persona, _, err := mt.coreSvc.GetBlock(tenantID, "persona", "")
	if err != nil {
		t.Fatalf("Failed to read persona block: %v", err)
	}
	if persona != existingPersona {
		t.Errorf("Expected persona to remain '%s', got '%s'", existingPersona, persona)
	}
}

func TestMigrateProfileToCoreMemory_NoProfileNoError(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath, WithMemoryProvider("letta"))
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	// No __me__ profile inserted — migration should be a no-op
	sess, err := mt.GetOrCreateSession("feishu", "chat_noprofile")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	persona, _, err := mt.coreSvc.GetBlock(sess.TenantID(), "persona", "")
	if err != nil {
		t.Fatalf("Failed to read persona block: %v", err)
	}
	if persona != "" {
		t.Errorf("Expected empty persona block when no profile, got '%s'", persona)
	}
}

func TestMultiTenantSession_LettaSessionRecall(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath, WithMemoryProvider("letta"))
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	sess, err := mt.GetOrCreateSession("feishu", "chat_letta")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Verify session uses LettaMemory
	if _, ok := sess.Memory().(*letta.LettaMemory); !ok {
		t.Fatal("Expected LettaMemory provider for letta mode")
	}

	// Recall should include core memory blocks
	ctx := context.Background()
	content, err := sess.Memory().Recall(ctx, "")
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if !strings.Contains(content, "Core Memory") {
		t.Errorf("Expected 'Core Memory' in recall, got: %s", content)
	}
}

func TestMultiTenantSession_RecallTimeRangeFunc(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath, WithMemoryProvider("letta"))
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	fn := mt.RecallTimeRangeFunc()
	if fn == nil {
		t.Fatal("Expected non-nil RecallTimeRangeFunc in letta mode")
	}
}

func TestMultiTenantSession_RecallTimeRangeFunc_NilForFlat(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath) // default flat mode
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	fn := mt.RecallTimeRangeFunc()
	if fn != nil {
		t.Error("Expected nil RecallTimeRangeFunc in flat mode")
	}
}
