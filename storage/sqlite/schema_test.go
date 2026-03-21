package sqlite

import (
	"database/sql"
	"testing"
)

func TestSchema_CreationFromZero(t *testing.T) {
	// Test creating a brand new database from scratch (version 0)
	dbPath := t.TempDir() + "/test_schema_create.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open (create) database: %v", err)
	}
	defer db.Close()

	// Verify schema_version is set to current
	var version int
	err = db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if err != nil {
		t.Fatalf("Failed to read schema version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("Expected schema version %d, got %d", schemaVersion, version)
	}

	// Verify key tables exist
	tables := []string{
		"tenants", "session_messages", "core_memory_blocks",
		"archival_memory", "event_history", "user_profiles",
		"cron_jobs", "user_llm_configs", "shared_registry", "user_settings",
	}
	for _, table := range tables {
		var name string
		err := db.Conn().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("Expected table %q to exist", table)
		} else if err != nil {
			t.Errorf("Error checking table %q: %v", table, err)
		}
	}
}

func TestSchema_MigrationFromV1(t *testing.T) {
	// Test full migration chain from v1 to current schemaVersion.
	// This is the most important migration path: it exercises all migration steps
	// (v1→2→3→4→5→6→8→9→10→11→12→13→14) and catches any ordering issues.
	dbPath := t.TempDir() + "/test_migrate_v1.db"

	// Create a minimal v1 database (the original schema before any migrations)
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	_, err = conn.Exec(`
		CREATE TABLE tenants (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_active_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(channel, chat_id)
		);
		CREATE TABLE session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			tool_call_id TEXT,
			tool_name TEXT,
			tool_arguments TEXT,
			tool_calls TEXT,
			detail TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
		);
		CREATE TABLE tenant_state (
			tenant_id INTEGER PRIMARY KEY,
			last_consolidated INTEGER DEFAULT 0,
			FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
		);
		CREATE TABLE event_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id INTEGER NOT NULL,
			entry TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
		);
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version (version) VALUES (1);
	`)
	if err != nil {
		t.Fatalf("Failed to create v1 baseline schema: %v", err)
	}
	conn.Close()

	// Open with Open() which should trigger full migration chain
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database for migration from v1: %v", err)
	}
	defer db.Close()

	// Verify final version
	var version int
	err = db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if err != nil {
		t.Fatalf("Failed to read schema version after migration: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("Expected schema version %d after migration from v1, got %d", schemaVersion, version)
	}
}

func TestSchema_AlreadyAtCurrentVersion(t *testing.T) {
	// Test that opening a database already at current version doesn't error
	dbPath := t.TempDir() + "/test_current.db"
	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	db1.Close()

	// Re-open should work fine (no migration needed)
	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to re-open database at current version: %v", err)
	}
	defer db2.Close()

	var version int
	err = db2.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if err != nil {
		t.Fatalf("Failed to read schema version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("Expected version %d, got %d", schemaVersion, version)
	}
}
