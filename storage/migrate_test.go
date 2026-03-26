package storage

import (
	"os"
	"path/filepath"
	"testing"

	"xbot/llm"
)

func TestShouldMigrate(t *testing.T) {
	// Empty directory - no migration needed
	emptyDir := t.TempDir()
	dbPath := filepath.Join(emptyDir, "xbot.db")
	if ShouldMigrate(emptyDir, dbPath) {
		t.Error("Expected no migration needed for empty directory")
	}

	// With database already exists - no migration needed
	withDB := t.TempDir()
	dbPath = filepath.Join(withDB, "xbot.db")
	if err := os.WriteFile(dbPath, []byte("sqlite db"), 0o644); err != nil {
		t.Fatalf("Failed to create dummy db file: %v", err)
	}
	if ShouldMigrate(withDB, dbPath) {
		t.Error("Expected no migration needed when database exists")
	}

	// With session.jsonl but no database - migration needed
	withSession := t.TempDir()
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	xbotDir := filepath.Join(withSession, ".xbot")
	if err := os.MkdirAll(xbotDir, 0o755); err != nil {
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		t.Fatalf("Failed to create .xbot directory: %v", err)
	}
	sessionPath := filepath.Join(xbotDir, "session.jsonl")
	if err := os.WriteFile(sessionPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("Failed to create session.jsonl: %v", err)
	}
	dbPath = filepath.Join(xbotDir, "xbot.db")
	if !ShouldMigrate(withSession, dbPath) {
		t.Error("Expected migration needed when session.jsonl exists")
	}

	// With MEMORY.md but no database - migration needed
	withMemory := t.TempDir()
	memoryPath := filepath.Join(withMemory, "MEMORY.md")
	if err := os.WriteFile(memoryPath, []byte("# Memory"), 0o644); err != nil {
		t.Fatalf("Failed to create MEMORY.md: %v", err)
	}
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	dbPath = filepath.Join(withMemory, ".xbot", "xbot.db")
	if !ShouldMigrate(withMemory, dbPath) {
		t.Error("Expected migration needed when MEMORY.md exists")
	}
}

func TestMigrateFromFileStorage(t *testing.T) {
	workDir := t.TempDir()
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	xbotDir := filepath.Join(workDir, ".xbot")
	if err := os.MkdirAll(xbotDir, 0o755); err != nil {
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		t.Fatalf("Failed to create .xbot directory: %v", err)
	}

	// Create legacy files
	sessionPath := filepath.Join(xbotDir, "session.jsonl")
	memoryPath := filepath.Join(workDir, "MEMORY.md")
	historyPath := filepath.Join(workDir, "HISTORY.md")
	dbPath := filepath.Join(xbotDir, "xbot.db")

	// Write sample session.jsonl
	msgs := []llm.ChatMessage{
		llm.NewUserMessage("Hello"),
		llm.NewAssistantMessage("Hi there"),
	}
	sessionData := ""
	for _, msg := range msgs {
		// Simple JSON encoding for test
		sessionData += `{"role":"` + msg.Role + `","content":"` + msg.Content + `"}` + "\n"
	}
	if err := os.WriteFile(sessionPath, []byte(sessionData), 0o644); err != nil {
		t.Fatalf("Failed to write session.jsonl: %v", err)
	}

	// Write MEMORY.md
	if err := os.WriteFile(memoryPath, []byte("# Long-term Memory\nUser likes Go"), 0o644); err != nil {
		t.Fatalf("Failed to write MEMORY.md: %v", err)
	}

	// Write HISTORY.md
	if err := os.WriteFile(historyPath, []byte("[2026-02-27] User asked about Go\n"), 0o644); err != nil {
		t.Fatalf("Failed to write HISTORY.md: %v", err)
	}

	// Run migration
	if err := MigrateFromFileStorage(workDir, dbPath); err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	// Verify database was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}

	// Verify original files were backed up
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		// File should be renamed ( backed up)
		t.Error("session.jsonl should be backed up (renamed)")
	}

	// Check for backup files
	matches, err := filepath.Glob(sessionPath + ".migrated-*")
	if err != nil {
		t.Fatalf("Failed to glob backup files: %v", err)
	}
	if len(matches) == 0 {
		t.Error("Expected backup file for session.jsonl")
	}
}

func TestMigrateFromFileStorage_NoLegacyFiles(t *testing.T) {
	workDir := t.TempDir()
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	xbotDir := filepath.Join(workDir, ".xbot")
	if err := os.MkdirAll(xbotDir, 0o755); err != nil {
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		t.Fatalf("Failed to create .xbot directory: %v", err)
	}

	dbPath := filepath.Join(xbotDir, "xbot.db")

	// Run migration with no legacy files - should succeed
	if err := MigrateFromFileStorage(workDir, dbPath); err != nil {
		t.Fatalf("Migration should succeed with no legacy files: %v", err)
	}

	// Verify database was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file should still be created even with no legacy data")
	}
}

func TestCLITenantConstants(t *testing.T) {
	// Verify constants are set correctly
	if CLITenantChannel != "cli" {
		t.Errorf("Expected CLITenantChannel 'cli', got '%s'", CLITenantChannel)
	}
	if CLITenantChatID != "direct" {
		t.Errorf("Expected CLITenantChatID 'direct', got '%s'", CLITenantChatID)
	}
}
