package sqlite

import (
	"database/sql"
	"testing"
)

// TestCoreMemoryService_TenantIsolation_Persona tests that persona is always stored at tenantID=0 (global).
func TestCoreMemoryService_TenantIsolation_Persona(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	// Create two different tenants
	tenantID1 := int64(100)
	tenantID2 := int64(200)

	// Initialize blocks for both tenants
	if err := svc.InitBlocks(tenantID1, "user1"); err != nil {
		t.Fatalf("InitBlocks tenant1 failed: %v", err)
	}
	if err := svc.InitBlocks(tenantID2, "user2"); err != nil {
		t.Fatalf("InitBlocks tenant2 failed: %v", err)
	}

	// Set persona for tenant1
	if err := svc.SetBlock(tenantID1, "persona", "Persona from tenant1", ""); err != nil {
		t.Fatalf("SetBlock persona tenant1 failed: %v", err)
	}

	// Set persona for tenant2
	if err := svc.SetBlock(tenantID2, "persona", "Persona from tenant2", ""); err != nil {
		t.Fatalf("SetBlock persona tenant2 failed: %v", err)
	}

	// Get persona - should return the LAST write (they share same tenantID=0)
	content1, _, err := svc.GetBlock(tenantID1, "persona", "")
	if err != nil {
		t.Fatalf("GetBlock persona tenant1 failed: %v", err)
	}

	content2, _, err := svc.GetBlock(tenantID2, "persona", "")
	if err != nil {
		t.Fatalf("GetBlock persona tenant2 failed: %v", err)
	}

	// Both should return the same content (tenantID=0 is global)
	if content1 != content2 {
		t.Errorf("Persona should be global (tenantID=0), got tenant1: %q, tenant2: %q", content1, content2)
	}

	// Verify GetAllBlocks also returns same persona
	blocks1, err := svc.GetAllBlocks(tenantID1, "")
	if err != nil {
		t.Fatalf("GetAllBlocks tenant1 failed: %v", err)
	}
	blocks2, err := svc.GetAllBlocks(tenantID2, "")
	if err != nil {
		t.Fatalf("GetAllBlocks tenant2 failed: %v", err)
	}

	if blocks1["persona"] != blocks2["persona"] {
		t.Errorf("GetAllBlocks persona should be same, got: %q vs %q", blocks1["persona"], blocks2["persona"])
	}
}

// TestCoreMemoryService_TenantIsolation_Human tests that human is cross-tenant (uses userID + tenantID=0).
func TestCoreMemoryService_TenantIsolation_Human(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	// Create two different tenants
	tenantID1 := int64(100)
	tenantID2 := int64(200)
	userID := "user_ou_123"

	// Initialize blocks for both tenants
	if err := svc.InitBlocks(tenantID1, userID); err != nil {
		t.Fatalf("InitBlocks tenant1 failed: %v", err)
	}
	if err := svc.InitBlocks(tenantID2, userID); err != nil {
		t.Fatalf("InitBlocks tenant2 failed: %v", err)
	}

	// Set human for tenant1
	if err := svc.SetBlock(tenantID1, "human", "Human from tenant1", userID); err != nil {
		t.Fatalf("SetBlock human tenant1 failed: %v", err)
	}

	// Set human for tenant2 (same userID)
	if err := svc.SetBlock(tenantID2, "human", "Human from tenant2", userID); err != nil {
		t.Fatalf("SetBlock human tenant2 failed: %v", err)
	}

	// Get human - should return the LAST write (they share same tenantID=0 + userID)
	content1, _, err := svc.GetBlock(tenantID1, "human", userID)
	if err != nil {
		t.Fatalf("GetBlock human tenant1 failed: %v", err)
	}

	content2, _, err := svc.GetBlock(tenantID2, "human", userID)
	if err != nil {
		t.Fatalf("GetBlock human tenant2 failed: %v", err)
	}

	// Both should return the same content (tenantID=0 + userID is cross-tenant)
	if content1 != content2 {
		t.Errorf("Human should be cross-tenant (tenantID=0), got tenant1: %q, tenant2: %q", content1, content2)
	}

	// Verify GetAllBlocks also returns same human
	blocks1, err := svc.GetAllBlocks(tenantID1, userID)
	if err != nil {
		t.Fatalf("GetAllBlocks tenant1 failed: %v", err)
	}
	blocks2, err := svc.GetAllBlocks(tenantID2, userID)
	if err != nil {
		t.Fatalf("GetAllBlocks tenant2 failed: %v", err)
	}

	if blocks1["human"] != blocks2["human"] {
		t.Errorf("GetAllBlocks human should be same, got: %q vs %q", blocks1["human"], blocks2["human"])
	}
}

// TestCoreMemoryService_TenantIsolation_WorkingContext tests that working_context is per-tenant.
func TestCoreMemoryService_TenantIsolation_WorkingContext(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	// Create two different tenants
	tenantID1 := int64(100)
	tenantID2 := int64(200)
	userID := "user_ou_123"

	// Initialize blocks for both tenants
	if err := svc.InitBlocks(tenantID1, userID); err != nil {
		t.Fatalf("InitBlocks tenant1 failed: %v", err)
	}
	if err := svc.InitBlocks(tenantID2, userID); err != nil {
		t.Fatalf("InitBlocks tenant2 failed: %v", err)
	}

	// Set working_context for tenant1
	if err := svc.SetBlock(tenantID1, "working_context", "Working context from tenant1", ""); err != nil {
		t.Fatalf("SetBlock working_context tenant1 failed: %v", err)
	}

	// Set working_context for tenant2
	if err := svc.SetBlock(tenantID2, "working_context", "Working context from tenant2", ""); err != nil {
		t.Fatalf("SetBlock working_context tenant2 failed: %v", err)
	}

	// Get working_context - should be different for each tenant
	content1, _, err := svc.GetBlock(tenantID1, "working_context", "")
	if err != nil {
		t.Fatalf("GetBlock working_context tenant1 failed: %v", err)
	}

	content2, _, err := svc.GetBlock(tenantID2, "working_context", "")
	if err != nil {
		t.Fatalf("GetBlock working_context tenant2 failed: %v", err)
	}

	// Each tenant should have its own working_context
	if content1 == content2 {
		t.Errorf("Working context should be per-tenant, got same: %q", content1)
	}

	if content1 != "Working context from tenant1" {
		t.Errorf("Expected 'Working context from tenant1', got: %q", content1)
	}

	if content2 != "Working context from tenant2" {
		t.Errorf("Expected 'Working context from tenant2', got: %q", content2)
	}

	// Verify GetAllBlocks also returns different working_context
	blocks1, err := svc.GetAllBlocks(tenantID1, userID)
	if err != nil {
		t.Fatalf("GetAllBlocks tenant1 failed: %v", err)
	}
	blocks2, err := svc.GetAllBlocks(tenantID2, userID)
	if err != nil {
		t.Fatalf("GetAllBlocks tenant2 failed: %v", err)
	}

	if blocks1["working_context"] == blocks2["working_context"] {
		t.Errorf("GetAllBlocks working_context should be different per tenant")
	}
}

// TestCoreMemoryService_ReadWriteConsistency tests that data written can be read back.
func TestCoreMemoryService_ReadWriteConsistency(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	tenantID := int64(100)
	userID := "user_ou_123"

	// Initialize
	if err := svc.InitBlocks(tenantID, userID); err != nil {
		t.Fatalf("InitBlocks failed: %v", err)
	}

	// Test persona
	if err := svc.SetBlock(tenantID, "persona", "Test persona", ""); err != nil {
		t.Fatalf("SetBlock persona failed: %v", err)
	}
	content, _, err := svc.GetBlock(tenantID, "persona", "")
	if err != nil {
		t.Fatalf("GetBlock persona failed: %v", err)
	}
	if content != "Test persona" {
		t.Errorf("Expected 'Test persona', got: %q", content)
	}

	// Test human
	if err := svc.SetBlock(tenantID, "human", "Test human", userID); err != nil {
		t.Fatalf("SetBlock human failed: %v", err)
	}
	content, _, err = svc.GetBlock(tenantID, "human", userID)
	if err != nil {
		t.Fatalf("GetBlock human failed: %v", err)
	}
	if content != "Test human" {
		t.Errorf("Expected 'Test human', got: %q", content)
	}

	// Test working_context
	if err := svc.SetBlock(tenantID, "working_context", "Test working context", ""); err != nil {
		t.Fatalf("SetBlock working_context failed: %v", err)
	}
	content, _, err = svc.GetBlock(tenantID, "working_context", "")
	if err != nil {
		t.Fatalf("GetBlock working_context failed: %v", err)
	}
	if content != "Test working context" {
		t.Errorf("Expected 'Test working context', got: %q", content)
	}

	// Test GetAllBlocks
	blocks, err := svc.GetAllBlocks(tenantID, userID)
	if err != nil {
		t.Fatalf("GetAllBlocks failed: %v", err)
	}
	if blocks["persona"] != "Test persona" {
		t.Errorf("GetAllBlocks persona: expected 'Test persona', got: %q", blocks["persona"])
	}
	if blocks["human"] != "Test human" {
		t.Errorf("GetAllBlocks human: expected 'Test human', got: %q", blocks["human"])
	}
	if blocks["working_context"] != "Test working context" {
		t.Errorf("GetAllBlocks working_context: expected 'Test working context', got: %q", blocks["working_context"])
	}
}

// TestCoreMemoryService_DefaultBlocks tests that default blocks are created on InitBlocks.
func TestCoreMemoryService_DefaultBlocks(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	tenantID := int64(100)
	userID := "user_ou_123"

	// Initialize - this should create default blocks
	if err := svc.InitBlocks(tenantID, userID); err != nil {
		t.Fatalf("InitBlocks failed: %v", err)
	}

	// Get blocks - should return default char limits
	_, charLimit, err := svc.GetBlock(tenantID, "persona", "")
	if err != nil {
		t.Fatalf("GetBlock persona failed: %v", err)
	}
	if charLimit != 2000 {
		t.Errorf("Expected persona char_limit 2000, got: %d", charLimit)
	}

	_, charLimit, err = svc.GetBlock(tenantID, "human", userID)
	if err != nil {
		t.Fatalf("GetBlock human failed: %v", err)
	}
	if charLimit != 2000 {
		t.Errorf("Expected human char_limit 2000, got: %d", charLimit)
	}

	_, charLimit, err = svc.GetBlock(tenantID, "working_context", "")
	if err != nil {
		t.Fatalf("GetBlock working_context failed: %v", err)
	}
	if charLimit != 4000 {
		t.Errorf("Expected working_context char_limit 4000, got: %d", charLimit)
	}
}

// TestCoreMemoryService_CharLimit tests content length validation.
func TestCoreMemoryService_CharLimit(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	tenantID := int64(100)
	userID := "user_ou_123"

	// Initialize
	if err := svc.InitBlocks(tenantID, userID); err != nil {
		t.Fatalf("InitBlocks failed: %v", err)
	}

	// Test content exceeds limit (persona limit is 2000)
	longContent := string(make([]byte, 2001))
	for i := range longContent {
		longContent = longContent[:i] + "a" + longContent[i+1:]
	}

	err = svc.SetBlock(tenantID, "persona", longContent, "")
	if err == nil {
		t.Error("Expected error when content exceeds limit, got nil")
	}
}

// TestCoreMemoryService_DifferentUsersHaveDifferentHuman tests that different users have different human blocks.
func TestCoreMemoryService_DifferentUsersHaveDifferentHuman(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	tenantID := int64(100)
	userID1 := "user_ou_123"
	userID2 := "user_ou_456"

	// Initialize for both users
	if err := svc.InitBlocks(tenantID, userID1); err != nil {
		t.Fatalf("InitBlocks user1 failed: %v", err)
	}
	if err := svc.InitBlocks(tenantID, userID2); err != nil {
		t.Fatalf("InitBlocks user2 failed: %v", err)
	}

	// Set different human content for each user
	if err := svc.SetBlock(tenantID, "human", "Human for user1", userID1); err != nil {
		t.Fatalf("SetBlock human user1 failed: %v", err)
	}
	if err := svc.SetBlock(tenantID, "human", "Human for user2", userID2); err != nil {
		t.Fatalf("SetBlock human user2 failed: %v", err)
	}

	// Get human for each user - should be different
	content1, _, err := svc.GetBlock(tenantID, "human", userID1)
	if err != nil {
		t.Fatalf("GetBlock human user1 failed: %v", err)
	}
	content2, _, err := svc.GetBlock(tenantID, "human", userID2)
	if err != nil {
		t.Fatalf("GetBlock human user2 failed: %v", err)
	}

	if content1 == content2 {
		t.Errorf("Different users should have different human blocks, got same: %q", content1)
	}

	if content1 != "Human for user1" {
		t.Errorf("Expected 'Human for user1', got: %q", content1)
	}
	if content2 != "Human for user2" {
		t.Errorf("Expected 'Human for user2', got: %q", content2)
	}
}

// TestCoreMemoryService_MigrationKeepsLongest tests that migration keeps the longest content.
func TestCoreMemoryService_MigrationKeepsLongest(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Insert legacy data directly (simulating old schema)
	conn := db.Conn()

	// Create tables
	_, err = conn.Exec(`
		CREATE TABLE IF NOT EXISTS core_memory_blocks (
			tenant_id INTEGER NOT NULL,
			block_name TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			content TEXT DEFAULT '',
			char_limit INTEGER DEFAULT 2000,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (tenant_id, block_name, user_id)
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	// Insert legacy persona data in different tenants
	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
		VALUES (1, 'persona', '', 'Short persona', 2000)
	`)
	if err != nil {
		t.Fatalf("Failed to insert persona1: %v", err)
	}
	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
		VALUES (2, 'persona', '', 'Much longer persona content here', 2000)
	`)
	if err != nil {
		t.Fatalf("Failed to insert persona2: %v", err)
	}

	// Insert legacy human data for same user in different tenants
	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
		VALUES (1, 'human', 'user_ou_123', 'Short human', 2000)
	`)
	if err != nil {
		t.Fatalf("Failed to insert human1: %v", err)
	}
	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
		VALUES (2, 'human', 'user_ou_123', 'Much longer human content here', 2000)
	`)
	if err != nil {
		t.Fatalf("Failed to insert human2: %v", err)
	}

	// Now create service and init (this triggers migration)
	svc := NewCoreMemoryService(db)
	tenantID := int64(100)
	userID := "user_ou_123"

	if err := svc.InitBlocks(tenantID, userID); err != nil {
		t.Fatalf("InitBlocks failed: %v", err)
	}

	// Check migration result - should keep longest
	personaContent, _, err := svc.GetBlock(tenantID, "persona", "")
	if err != nil {
		t.Fatalf("GetBlock persona failed: %v", err)
	}
	if personaContent != "Much longer persona content here" {
		t.Errorf("Expected longest persona 'Much longer persona content here', got: %q", personaContent)
	}

	humanContent, _, err := svc.GetBlock(tenantID, "human", userID)
	if err != nil {
		t.Fatalf("GetBlock human failed: %v", err)
	}
	if humanContent != "Much longer human content here" {
		t.Errorf("Expected longest human 'Much longer human content here', got: %q", humanContent)
	}

	// Verify old data is cleaned up
	var count int
	err = conn.QueryRow(`
		SELECT COUNT(*) FROM core_memory_blocks 
		WHERE block_name IN ('persona', 'human') AND tenant_id != 0
	`).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("Failed to check old data: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 legacy records, got: %d", count)
	}
}
