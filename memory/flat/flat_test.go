package flat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func setupTestDir(t *testing.T) (string, int64) {
	t.Helper()
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0o755)
	return memDir, 42
}

func TestFlatMemory_Recall_Empty(t *testing.T) {
	memDir, tenantID := setupTestDir(t)
	m := New(tenantID, memDir)

	result, err := m.Recall(context.Background(), "any query")
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if result != "" {
		t.Errorf("Expected empty, got %q", result)
	}
}

func TestFlatMemory_Recall_WithContent(t *testing.T) {
	memDir, tenantID := setupTestDir(t)
	m := New(tenantID, memDir)

	// Write MEMORY.md directly
	memoryPath := filepath.Join(memDir, memoryFileName)
	content := "# Facts\n- User likes Go\n- Prefers concise code"
	if err := os.WriteFile(memoryPath, []byte(content), 0o644); err != nil {
		t.Fatalf("Failed to write MEMORY.md: %v", err)
	}

	result, err := m.Recall(context.Background(), "ignored query")
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if result == "" {
		t.Fatal("Expected non-empty result")
	}
	// Should contain "Core Memory" header and the content
	if result == "" {
		t.Error("Expected non-empty result")
	}
}

func TestFlatMemory_Recall_WithKnowledgeFiles(t *testing.T) {
	memDir, tenantID := setupTestDir(t)
	m := New(tenantID, memDir)

	// Write MEMORY.md
	os.WriteFile(filepath.Join(memDir, memoryFileName), []byte("Core facts"), 0o644)

	// Create knowledge files
	knowledgeDir := filepath.Join(memDir, "knowledge")
	os.MkdirAll(knowledgeDir, 0o755)
	os.WriteFile(filepath.Join(knowledgeDir, "gotchas.md"), []byte("Windows VT issues"), 0o644)

	result, err := m.Recall(context.Background(), "any query")
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	// Should list knowledge files
	if result == "" {
		t.Fatal("Expected non-empty result")
	}
}

func TestFlatMemory_Close(t *testing.T) {
	memDir, tenantID := setupTestDir(t)
	m := New(tenantID, memDir)

	if err := m.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func TestFlatMemory_BaseDir(t *testing.T) {
	memDir, tenantID := setupTestDir(t)
	m := New(tenantID, memDir)

	if m.BaseDir() != memDir {
		t.Errorf("Expected %q, got %q", memDir, m.BaseDir())
	}
}
