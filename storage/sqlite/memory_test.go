package sqlite

import (
	"testing"
)

func TestMemoryService_LongTermMemory(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	memorySvc := NewMemoryService(db)

	// Create tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Initially no memory
	content, err := memorySvc.ReadLongTerm(tenantID)
	if err != nil {
		t.Fatalf("Failed to read long-term memory: %v", err)
	}
	if content != "" {
		t.Errorf("Expected empty memory initially, got '%s'", content)
	}

	// Write memory
	testMemory := "# Key Facts\n- User likes Go\n- Project name is xbot"
	if err := memorySvc.WriteLongTerm(tenantID, testMemory); err != nil {
		t.Fatalf("Failed to write long-term memory: %v", err)
	}

	// Read back
	content, err = memorySvc.ReadLongTerm(tenantID)
	if err != nil {
		t.Fatalf("Failed to read long-term memory: %v", err)
	}
	if content != testMemory {
		t.Errorf("Expected memory '%s', got '%s'", testMemory, content)
	}

	// Overwrite memory
	newMemory := "# Updated Facts\n- User loves Go"
	if err := memorySvc.WriteLongTerm(tenantID, newMemory); err != nil {
		t.Fatalf("Failed to overwrite long-term memory: %v", err)
	}

	content, err = memorySvc.ReadLongTerm(tenantID)
	if err != nil {
		t.Fatalf("Failed to read updated memory: %v", err)
	}
	if content != newMemory {
		t.Errorf("Expected updated memory '%s', got '%s'", newMemory, content)
	}
}

func TestMemoryService_EventHistory(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	memorySvc := NewMemoryService(db)

	// Create two tenants
	tenantID1, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant 1: %v", err)
	}
	tenantID2, err := tenantSvc.GetOrCreateTenantID("test", "chat2")
	if err != nil {
		t.Fatalf("Failed to create tenant 2: %v", err)
	}

	// Add history entries to tenant 1
	entry1 := "[2026-02-27 10:00] User asked about Go programming"
	entry2 := "[2026-02-27 11:00] Discussed SQLite implementation"
	if err := memorySvc.AppendHistory(tenantID1, entry1); err != nil {
		t.Fatalf("Failed to append history entry 1: %v", err)
	}
	if err := memorySvc.AppendHistory(tenantID1, entry2); err != nil {
		t.Fatalf("Failed to append history entry 2: %v", err)
	}

	// Add history entry to tenant 2
	entry3 := "[2026-02-27 12:00] Different conversation"
	if err := memorySvc.AppendHistory(tenantID2, entry3); err != nil {
		t.Fatalf("Failed to append history entry 3: %v", err)
	}

	// Get history for tenant 1
	entries1, err := memorySvc.GetHistoryEntries(tenantID1, 10)
	if err != nil {
		t.Fatalf("Failed to get history entries for tenant 1: %v", err)
	}
	if len(entries1) != 2 {
		t.Errorf("Expected 2 history entries for tenant 1, got %d", len(entries1))
	}

	// Get history for tenant 2
	entries2, err := memorySvc.GetHistoryEntries(tenantID2, 10)
	if err != nil {
		t.Fatalf("Failed to get history entries for tenant 2: %v", err)
	}
	if len(entries2) != 1 {
		t.Errorf("Expected 1 history entry for tenant 2, got %d", len(entries2))
	}

	// Test limit
	limitedEntries, err := memorySvc.GetHistoryEntries(tenantID1, 1)
	if err != nil {
		t.Fatalf("Failed to get limited history entries: %v", err)
	}
	if len(limitedEntries) != 1 {
		t.Errorf("Expected 1 limited entry, got %d", len(limitedEntries))
	}
}

func TestMemoryService_State(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	memorySvc := NewMemoryService(db)

	// Create tenant
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Initially state should be 0
	state, err := memorySvc.GetState(tenantID)
	if err != nil {
		t.Fatalf("Failed to get state: %v", err)
	}
	if state != 0 {
		t.Errorf("Expected initial state 0, got %d", state)
	}

	// Update state
	if err := memorySvc.SetState(tenantID, 42); err != nil {
		t.Fatalf("Failed to set state: %v", err)
	}

	// Read back state
	state, err = memorySvc.GetState(tenantID)
	if err != nil {
		t.Fatalf("Failed to get updated state: %v", err)
	}
	if state != 42 {
		t.Errorf("Expected state 42, got %d", state)
	}

	// Update again
	if err := memorySvc.SetState(tenantID, 100); err != nil {
		t.Fatalf("Failed to update state: %v", err)
	}

	state, err = memorySvc.GetState(tenantID)
	if err != nil {
		t.Fatalf("Failed to get final state: %v", err)
	}
	if state != 100 {
		t.Errorf("Expected final state 100, got %d", state)
	}
}
