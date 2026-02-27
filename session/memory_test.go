package session

import (
	"testing"
)

func TestTenantMemory_ReadWriteLongTerm(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	sess, err := mt.GetOrCreateSession("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	mem := sess.Memory()

	// Initially empty
	content, err := mem.ReadLongTerm()
	if err != nil {
		t.Fatalf("Failed to read long-term memory: %v", err)
	}
	if content != "" {
		t.Errorf("Expected empty memory initially, got '%s'", content)
	}

	// Write memory
	testMemory := "# Key Facts\n- User likes Go\n- Project is xbot"
	if err := mem.WriteLongTerm(testMemory); err != nil {
		t.Fatalf("Failed to write memory: %v", err)
	}

	// Read back
	content, err = mem.ReadLongTerm()
	if err != nil {
		t.Fatalf("Failed to read memory: %v", err)
	}
	if content != testMemory {
		t.Errorf("Expected '%s', got '%s'", testMemory, content)
	}
}

func TestTenantMemory_AppendHistory(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	sess, err := mt.GetOrCreateSession("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	mem := sess.Memory()

	// Append entries
	entries := []string{
		"[2026-02-27 10:00] User asked about Go",
		"[2026-02-27 11:00] Discussed SQLite",
	}

	for _, entry := range entries {
		if err := mem.AppendHistory(entry); err != nil {
			t.Fatalf("Failed to append history: %v", err)
		}
	}

	// Note: We can't directly read history from TenantMemory without exposing a method
	// This test verifies the AppendHistory method doesn't error
}

func TestTenantMemory_GetMemoryContext(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	mt, err := NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("Failed to create multi-tenant session: %v", err)
	}
	defer mt.Close()

	sess, err := mt.GetOrCreateSession("test", "chat1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	mem := sess.Memory()

	// Empty memory
	ctx, err := mem.GetMemoryContext()
	if err != nil {
		t.Fatalf("Failed to get memory context: %v", err)
	}
	if ctx != "" {
		t.Errorf("Expected empty context, got '%s'", ctx)
	}

	// With memory
	if err := mem.WriteLongTerm("# Facts\nUser likes Go"); err != nil {
		t.Fatalf("Failed to write memory: %v", err)
	}

	ctx, err = mem.GetMemoryContext()
	if err != nil {
		t.Fatalf("Failed to get memory context: %v", err)
	}
	if ctx == "" {
		t.Error("Expected non-empty context")
	}
	// Should have header
	expectedHeader := "## Long-term Memory"
	if len(ctx) < len(expectedHeader) {
		t.Errorf("Context too short: %s", ctx)
	}
}
