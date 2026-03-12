package flat

import (
	"context"
	"testing"

	"xbot/storage/sqlite"
)

func setupTestDB(t *testing.T) (*sqlite.DB, int64) {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	tenantSvc := sqlite.NewTenantService(db)
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}
	return db, tenantID
}

func TestFlatMemory_Recall_Empty(t *testing.T) {
	db, tenantID := setupTestDB(t)
	memorySvc := sqlite.NewMemoryService(db)
	m := New(tenantID, memorySvc, nil)

	result, err := m.Recall(context.Background(), "any query")
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if result != "" {
		t.Errorf("Expected empty, got %q", result)
	}
}

func TestFlatMemory_Recall_WithContent(t *testing.T) {
	db, tenantID := setupTestDB(t)
	memorySvc := sqlite.NewMemoryService(db)
	m := New(tenantID, memorySvc, nil)

	if err := memorySvc.WriteLongTerm(tenantID, "# Facts\nUser likes Go"); err != nil {
		t.Fatalf("WriteLongTerm failed: %v", err)
	}

	result, err := m.Recall(context.Background(), "ignored query")
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if result == "" {
		t.Fatal("Expected non-empty result")
	}
	expected := "## Long-term Memory\n# Facts\nUser likes Go"
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestFlatMemory_Close(t *testing.T) {
	db, tenantID := setupTestDB(t)
	memorySvc := sqlite.NewMemoryService(db)
	m := New(tenantID, memorySvc, nil)

	if err := m.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}
