package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	log "xbot/logger"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite database connection with schema management
type DB struct {
	conn *sql.DB
	path string
	mu   sync.RWMutex
}

const schemaVersion = 6

// Open opens or creates a SQLite database at the given path
// If the database doesn't exist, it will be created with the required schema
func Open(path string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Set connection pool settings
	conn.SetMaxOpenConns(1) // SQLite works best with a single connection
	conn.SetMaxIdleConns(1)

	db := &DB{
		conn: conn,
		path: path,
	}

	// Initialize schema
	if err := db.initSchema(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}

	log.WithField("path", path).Info("SQLite database opened")
	return db, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.conn != nil {
		if err := db.conn.Close(); err != nil {
			return fmt.Errorf("close database: %w", err)
		}
		db.conn = nil
	}
	return nil
}

// Conn returns the underlying database connection
func (db *DB) Conn() *sql.DB {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.conn
}

// initSchema creates the database schema if it doesn't exist, and runs migrations
func (db *DB) initSchema() error {
	conn := db.Conn()

	// Check if schema already exists by checking tenants table
	var tableName string
	err := conn.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='tenants'").Scan(&tableName)
	if err == sql.ErrNoRows {
		return db.createSchema()
	}
	if err != nil {
		return fmt.Errorf("check schema: %w", err)
	}

	// Schema exists — check version and run migrations
	var version int
	err = conn.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if err != nil {
		version = 1
	}
	if version < schemaVersion {
		return db.migrateSchema(version)
	}
	return nil
}

func (db *DB) createSchema() error {
	schema := `
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
CREATE INDEX idx_session_messages_tenant_created ON session_messages(tenant_id, created_at);

CREATE TABLE tenant_state (
    tenant_id INTEGER PRIMARY KEY,
    last_consolidated INTEGER DEFAULT 0,
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

CREATE TABLE long_term_memory (
    tenant_id INTEGER PRIMARY KEY,
    content TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

CREATE TABLE event_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    entry TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX idx_event_history_tenant_created ON event_history(tenant_id, created_at);

CREATE TABLE user_profiles (
    sender_id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    profile TEXT NOT NULL DEFAULT '',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE core_memory_blocks (
    tenant_id INTEGER NOT NULL,
    block_name TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    char_limit INTEGER NOT NULL DEFAULT 2000,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, block_name),
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

CREATE TABLE archival_memory (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    content TEXT NOT NULL,
    embedding BLOB,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX idx_archival_memory_tenant ON archival_memory(tenant_id);

CREATE VIRTUAL TABLE IF NOT EXISTS event_history_fts USING fts5(
    entry,
    content='event_history',
    content_rowid='id'
);

CREATE TRIGGER event_history_ai AFTER INSERT ON event_history BEGIN
    INSERT INTO event_history_fts(rowid, entry) VALUES (new.id, new.entry);
END;

CREATE TABLE schema_version (
    version INTEGER PRIMARY KEY
);
INSERT INTO schema_version (version) VALUES (6);

CREATE TABLE user_llm_configs (
    sender_id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    base_url TEXT NOT NULL,
    api_key TEXT NOT NULL,
    model TEXT,
    user_id TEXT,
    enterprise_id TEXT,
    domain TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cron_jobs (
    id TEXT PRIMARY KEY,
    message TEXT NOT NULL,
    channel TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    sender_id TEXT NOT NULL DEFAULT '',
    cron_expr TEXT,
    every_seconds INTEGER DEFAULT 0,
    delay_seconds INTEGER DEFAULT 0,
    at TEXT,
    created_at DATETIME NOT NULL,
    next_run DATETIME NOT NULL,
    last_trigger DATETIME,
    one_shot INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_cron_jobs_next_run ON cron_jobs(next_run);
CREATE INDEX idx_cron_jobs_sender ON cron_jobs(sender_id);
`
	if _, err := db.Conn().Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	log.Info("Database schema initialized (v2)")
	return nil
}

func (db *DB) migrateSchema(from int) error {
	conn := db.Conn()

	if from < 2 {
		migration := `
CREATE TABLE IF NOT EXISTS user_profiles (
    sender_id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    profile TEXT NOT NULL DEFAULT '',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
UPDATE schema_version SET version = 2;
`
		if _, err := conn.Exec(migration); err != nil {
			return fmt.Errorf("migrate v1->v2: %w", err)
		}
		log.Info("Database migrated to v2 (added user_profiles)")
	}

	if from < 3 {
		migration := `
CREATE TABLE IF NOT EXISTS core_memory_blocks (
    tenant_id INTEGER NOT NULL,
    block_name TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    char_limit INTEGER NOT NULL DEFAULT 2000,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, block_name),
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS archival_memory (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    content TEXT NOT NULL,
    embedding BLOB,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_archival_memory_tenant ON archival_memory(tenant_id);

CREATE VIRTUAL TABLE IF NOT EXISTS event_history_fts USING fts5(
    entry,
    content='event_history',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS event_history_ai AFTER INSERT ON event_history BEGIN
    INSERT INTO event_history_fts(rowid, entry) VALUES (new.id, new.entry);
END;

UPDATE schema_version SET version = 3;
`
		if _, err := conn.Exec(migration); err != nil {
			return fmt.Errorf("migrate v2->v3: %w", err)
		}

		// Backfill FTS index from existing event_history entries
		if _, err := conn.Exec(`INSERT INTO event_history_fts(rowid, entry) SELECT id, entry FROM event_history`); err != nil {
			log.WithError(err).Warn("Failed to backfill event_history_fts (may already be populated)")
		}

		log.Info("Database migrated to v3 (added core_memory_blocks, archival_memory, event_history_fts)")
	}

	if from < 4 {
		migration := `
CREATE TABLE IF NOT EXISTS cron_jobs (
    id TEXT PRIMARY KEY,
    message TEXT NOT NULL,
    channel TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    sender_id TEXT NOT NULL DEFAULT '',
    cron_expr TEXT,
    every_seconds INTEGER DEFAULT 0,
    delay_seconds INTEGER DEFAULT 0,
    at TEXT,
    created_at DATETIME NOT NULL,
    next_run DATETIME NOT NULL,
    one_shot INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_next_run ON cron_jobs(next_run);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_sender ON cron_jobs(sender_id);

UPDATE schema_version SET version = 4;
`
		if _, err := conn.Exec(migration); err != nil {
			return fmt.Errorf("migrate v3->v4: %w", err)
		}
		log.Info("Database migrated to v4 (added cron_jobs)")
	}

	if from < 5 {
		// Check if column already exists before adding
		var count int
		err := conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('cron_jobs') WHERE name = 'last_trigger'").Scan(&count)
		if err == nil && count == 0 {
			// Column doesn't exist, add it
			_, err = conn.Exec("ALTER TABLE cron_jobs ADD COLUMN last_trigger DATETIME")
			if err != nil {
				return fmt.Errorf("migrate v4->v5: %w", err)
			}
			log.Info("Database migrated to v5 (added last_trigger to cron_jobs)")
		}
		// Always update version even if column exists (for fresh databases)
		if _, err := conn.Exec("UPDATE schema_version SET version = 5"); err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
		if from < 5 {
			log.Info("Database migrated to v5")
		}
	}

	if from < 6 {
		migration := `
CREATE TABLE IF NOT EXISTS user_llm_configs (
    sender_id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    base_url TEXT NOT NULL,
    api_key TEXT NOT NULL,
    model TEXT,
    user_id TEXT,
    enterprise_id TEXT,
    domain TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

UPDATE schema_version SET version = 6;
`
		if _, err := conn.Exec(migration); err != nil {
			return fmt.Errorf("migrate v5->v6: %w", err)
		}
		log.Info("Database migrated to v6 (added user_llm_configs)")
	}

	return nil
}
