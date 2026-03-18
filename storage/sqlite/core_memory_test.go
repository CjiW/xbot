package sqlite

import (
	"database/sql"
	"testing"
)

// TestCoreMemoryService_PersonaPerTenant tests that persona is per-tenant isolated.
// Each tenant/Agent/SubAgent has its own independent persona block.
func TestCoreMemoryService_PersonaPerTenant(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	tenantID1 := int64(100)
	tenantID2 := int64(200)

	// Initialize both tenants
	if err := svc.InitBlocks(tenantID1, ""); err != nil {
		t.Fatalf("InitBlocks tenant1 failed: %v", err)
	}
	if err := svc.InitBlocks(tenantID2, ""); err != nil {
		t.Fatalf("InitBlocks tenant2 failed: %v", err)
	}

	// Set persona from tenant1
	if err := svc.SetBlock(tenantID1, "persona", "Persona from tenant1", ""); err != nil {
		t.Fatalf("SetBlock persona tenant1 failed: %v", err)
	}

	// Set persona from tenant2 (should NOT overwrite tenant1's - they are isolated)
	if err := svc.SetBlock(tenantID2, "persona", "Persona from tenant2", ""); err != nil {
		t.Fatalf("SetBlock persona tenant2 failed: %v", err)
	}

	// Each tenant should read its own persona
	content1, _, err := svc.GetBlock(tenantID1, "persona", "")
	if err != nil {
		t.Fatalf("GetBlock persona tenant1 failed: %v", err)
	}
	content2, _, err := svc.GetBlock(tenantID2, "persona", "")
	if err != nil {
		t.Fatalf("GetBlock persona tenant2 failed: %v", err)
	}

	// Personas should be different (per-tenant isolation)
	if content1 == content2 {
		t.Errorf("Persona should be per-tenant isolated, both got: %q", content1)
	}
	if content1 != "Persona from tenant1" {
		t.Errorf("Expected 'Persona from tenant1', got: %q", content1)
	}
	if content2 != "Persona from tenant2" {
		t.Errorf("Expected 'Persona from tenant2', got: %q", content2)
	}

	// GetAllBlocks also returns different persona per tenant
	blocks1, _ := svc.GetAllBlocks(tenantID1, "")
	blocks2, _ := svc.GetAllBlocks(tenantID2, "")
	if blocks1["persona"] == blocks2["persona"] {
		t.Errorf("GetAllBlocks persona should be different: %q vs %q", blocks1["persona"], blocks2["persona"])
	}
}

// TestCoreMemoryService_HumanCrossTenant tests that human is cross-tenant (tenantID=0 + userID).
func TestCoreMemoryService_HumanCrossTenant(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	tenantID1 := int64(100)
	tenantID2 := int64(200)
	userID := "ou_123"

	// Initialize both tenants
	if err := svc.InitBlocks(tenantID1, userID); err != nil {
		t.Fatalf("InitBlocks tenant1 failed: %v", err)
	}
	if err := svc.InitBlocks(tenantID2, userID); err != nil {
		t.Fatalf("InitBlocks tenant2 failed: %v", err)
	}

	// Set human from tenant1
	if err := svc.SetBlock(tenantID1, "human", "Human from tenant1", userID); err != nil {
		t.Fatalf("SetBlock human tenant1 failed: %v", err)
	}

	// Set human from tenant2 (same userID, should overwrite)
	if err := svc.SetBlock(tenantID2, "human", "Human from tenant2", userID); err != nil {
		t.Fatalf("SetBlock human tenant2 failed: %v", err)
	}

	// Both tenants should read same human (cross-tenant)
	content1, _, err := svc.GetBlock(tenantID1, "human", userID)
	if err != nil {
		t.Fatalf("GetBlock human tenant1 failed: %v", err)
	}
	content2, _, err := svc.GetBlock(tenantID2, "human", userID)
	if err != nil {
		t.Fatalf("GetBlock human tenant2 failed: %v", err)
	}

	if content1 != content2 {
		t.Errorf("Human should be cross-tenant, got tenant1: %q, tenant2: %q", content1, content2)
	}
	if content1 != "Human from tenant2" {
		t.Errorf("Expected last write 'Human from tenant2', got: %q", content1)
	}
}

// TestCoreMemoryService_WorkingContextPerTenant tests that working_context is per-tenant isolated.
func TestCoreMemoryService_WorkingContextPerTenant(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	tenantID1 := int64(100)
	tenantID2 := int64(200)

	// Initialize both tenants
	if err := svc.InitBlocks(tenantID1, ""); err != nil {
		t.Fatalf("InitBlocks tenant1 failed: %v", err)
	}
	if err := svc.InitBlocks(tenantID2, ""); err != nil {
		t.Fatalf("InitBlocks tenant2 failed: %v", err)
	}

	// Set working_context for each tenant
	if err := svc.SetBlock(tenantID1, "working_context", "WC tenant1", ""); err != nil {
		t.Fatalf("SetBlock tenant1 failed: %v", err)
	}
	if err := svc.SetBlock(tenantID2, "working_context", "WC tenant2", ""); err != nil {
		t.Fatalf("SetBlock tenant2 failed: %v", err)
	}

	// Each tenant should have its own working_context
	content1, _, err := svc.GetBlock(tenantID1, "working_context", "")
	if err != nil {
		t.Fatalf("GetBlock tenant1 failed: %v", err)
	}
	content2, _, err := svc.GetBlock(tenantID2, "working_context", "")
	if err != nil {
		t.Fatalf("GetBlock tenant2 failed: %v", err)
	}

	if content1 == content2 {
		t.Errorf("Working context should be per-tenant, got same: %q", content1)
	}
	if content1 != "WC tenant1" {
		t.Errorf("Expected 'WC tenant1', got: %q", content1)
	}
	if content2 != "WC tenant2" {
		t.Errorf("Expected 'WC tenant2', got: %q", content2)
	}

	// GetAllBlocks also returns different working_context
	blocks1, _ := svc.GetAllBlocks(tenantID1, "")
	blocks2, _ := svc.GetAllBlocks(tenantID2, "")
	if blocks1["working_context"] == blocks2["working_context"] {
		t.Error("GetAllBlocks working_context should be different per tenant")
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
	userID := "ou_123"

	if err := svc.InitBlocks(tenantID, userID); err != nil {
		t.Fatalf("InitBlocks failed: %v", err)
	}

	// Test all three block types
	testCases := []struct {
		blockName string
		content   string
		userID    string
	}{
		{"persona", "Test persona", ""},
		{"human", "Test human", userID},
		{"working_context", "Test working context", ""},
	}

	for _, tc := range testCases {
		if err := svc.SetBlock(tenantID, tc.blockName, tc.content, tc.userID); err != nil {
			t.Fatalf("SetBlock %s failed: %v", tc.blockName, err)
		}

		got, _, err := svc.GetBlock(tenantID, tc.blockName, tc.userID)
		if err != nil {
			t.Fatalf("GetBlock %s failed: %v", tc.blockName, err)
		}
		if got != tc.content {
			t.Errorf("%s: expected %q, got %q", tc.blockName, tc.content, got)
		}
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

// TestCoreMemoryService_DefaultBlocks tests that default char limits are set correctly.
func TestCoreMemoryService_DefaultBlocks(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	tenantID := int64(100)
	userID := "ou_123"

	if err := svc.InitBlocks(tenantID, userID); err != nil {
		t.Fatalf("InitBlocks failed: %v", err)
	}

	// Check default char limits
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

	if err := svc.InitBlocks(tenantID, ""); err != nil {
		t.Fatalf("InitBlocks failed: %v", err)
	}

	// persona limit is 2000
	longContent := ""
	for i := 0; i < 2001; i++ {
		longContent += "a"
	}

	err = svc.SetBlock(tenantID, "persona", longContent, "")
	if err == nil {
		t.Error("Expected error when content exceeds limit, got nil")
	}
}

// TestCoreMemoryService_DifferentUsersDifferentHuman tests that different users have different human blocks.
func TestCoreMemoryService_DifferentUsersDifferentHuman(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	svc := NewCoreMemoryService(db)

	tenantID := int64(100)
	userID1 := "ou_123"
	userID2 := "ou_456"

	// Initialize for both users
	if err := svc.InitBlocks(tenantID, userID1); err != nil {
		t.Fatalf("InitBlocks user1 failed: %v", err)
	}
	if err := svc.InitBlocks(tenantID, userID2); err != nil {
		t.Fatalf("InitBlocks user2 failed: %v", err)
	}

	// Set different human content
	if err := svc.SetBlock(tenantID, "human", "Human for user1", userID1); err != nil {
		t.Fatalf("SetBlock human user1 failed: %v", err)
	}
	if err := svc.SetBlock(tenantID, "human", "Human for user2", userID2); err != nil {
		t.Fatalf("SetBlock human user2 failed: %v", err)
	}

	// Each user should have their own human block
	content1, _, err := svc.GetBlock(tenantID, "human", userID1)
	if err != nil {
		t.Fatalf("GetBlock human user1 failed: %v", err)
	}
	content2, _, err := svc.GetBlock(tenantID, "human", userID2)
	if err != nil {
		t.Fatalf("GetBlock human user2 failed: %v", err)
	}

	if content1 == content2 {
		t.Errorf("Different users should have different human blocks")
	}
	if content1 != "Human for user1" {
		t.Errorf("Expected 'Human for user1', got: %q", content1)
	}
	if content2 != "Human for user2" {
		t.Errorf("Expected 'Human for user2', got: %q", content2)
	}
}

// TestCoreMemoryService_MigrationKeepsLongestHuman tests that migration keeps the longest human content.
// Note: persona is no longer migrated - each tenant keeps its own persona.
func TestCoreMemoryService_MigrationKeepsLongestHuman(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	conn := db.Conn()

	// Create table manually (simulating old schema)
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

	// Insert legacy persona data in different tenants (these should stay at their original tenantID)
	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
		VALUES (1, 'persona', '', 'Short', 2000)
	`)
	if err != nil {
		t.Fatalf("Failed to insert persona1: %v", err)
	}
	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
		VALUES (2, 'persona', '', 'Much longer persona content', 2000)
	`)
	if err != nil {
		t.Fatalf("Failed to insert persona2: %v", err)
	}

	// Insert legacy human data for same user in different tenants
	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
		VALUES (1, 'human', 'ou_123', 'Short human', 2000)
	`)
	if err != nil {
		t.Fatalf("Failed to insert human1: %v", err)
	}
	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
		VALUES (2, 'human', 'ou_123', 'Much longer human content here', 2000)
	`)
	if err != nil {
		t.Fatalf("Failed to insert human2: %v", err)
	}

	// Now create service and init (triggers migration)
	svc := NewCoreMemoryService(db)
	userID := "ou_123"

	if err := svc.InitBlocks(100, userID); err != nil {
		t.Fatalf("InitBlocks failed: %v", err)
	}

	// Check migration result for human - should keep longest at tenantID=0
	humanContent, _, err := svc.GetBlock(100, "human", userID)
	if err != nil {
		t.Fatalf("GetBlock human failed: %v", err)
	}
	if humanContent != "Much longer human content here" {
		t.Errorf("Expected longest human, got: %q", humanContent)
	}

	// Verify persona data is NOT migrated - each tenant keeps its own
	// Tenant 1's persona should still be at tenantID=1
	var persona1 string
	err = conn.QueryRow(`
		SELECT content FROM core_memory_blocks 
		WHERE tenant_id = 1 AND block_name = 'persona' AND user_id = ''
	`).Scan(&persona1)
	if err != nil {
		t.Fatalf("Failed to query persona1: %v", err)
	}
	if persona1 != "Short" {
		t.Errorf("Expected persona1 'Short', got: %q", persona1)
	}

	// Tenant 2's persona should still be at tenantID=2
	var persona2 string
	err = conn.QueryRow(`
		SELECT content FROM core_memory_blocks 
		WHERE tenant_id = 2 AND block_name = 'persona' AND user_id = ''
	`).Scan(&persona2)
	if err != nil {
		t.Fatalf("Failed to query persona2: %v", err)
	}
	if persona2 != "Much longer persona content" {
		t.Errorf("Expected persona2 'Much longer persona content', got: %q", persona2)
	}

	// Verify old human data is cleaned up (only human, not persona)
	var count int
	err = conn.QueryRow(`
		SELECT COUNT(*) FROM core_memory_blocks 
		WHERE block_name = 'human' AND tenant_id != 0
	`).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("Failed to check old data: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 legacy human records, got: %d", count)
	}
}
