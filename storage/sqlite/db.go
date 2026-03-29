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

const schemaVersion = 17

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

	// P-08 修复：启用 WAL 模式提升并发读性能和韧性
	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	// 设置 busy_timeout 为 5 秒，避免 "database is locked" 错误
	if _, err := conn.Exec("PRAGMA busy_timeout=5000"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

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
    user_id TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    char_limit INTEGER NOT NULL DEFAULT 2000,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, block_name, user_id),
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
INSERT INTO schema_version (version) VALUES (17);

CREATE TABLE runner_tokens (
    user_id     TEXT PRIMARY KEY,
    token       TEXT NOT NULL,
    mode        TEXT NOT NULL DEFAULT 'native',
    docker_image TEXT NOT NULL DEFAULT '',
    workspace   TEXT NOT NULL DEFAULT '/workspace',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE runners (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    token        TEXT    NOT NULL UNIQUE,
    mode         TEXT    NOT NULL DEFAULT 'native',
    docker_image TEXT    NOT NULL DEFAULT 'ubuntu:22.04',
    workspace    TEXT    NOT NULL DEFAULT '',
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, name)
);

CREATE TABLE shared_registry (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL CHECK(type IN ('skill', 'agent')),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    author      TEXT NOT NULL,
    tags        TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL,
    sharing     TEXT NOT NULL DEFAULT 'private' CHECK(sharing IN ('private', 'public')),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE(type, name, author)
);
CREATE INDEX idx_shared_type_sharing ON shared_registry(type, sharing);
CREATE INDEX idx_shared_author ON shared_registry(author);

CREATE TABLE web_users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    username   TEXT NOT NULL UNIQUE,
    password   TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE user_settings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    channel    TEXT NOT NULL,
    sender_id  TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    UNIQUE(channel, sender_id, key)
);
CREATE INDEX idx_user_settings_sender ON user_settings(channel, sender_id);
CREATE TABLE user_llm_configs (
    sender_id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    base_url TEXT NOT NULL,
    api_key TEXT NOT NULL,
    model TEXT,
    max_context INTEGER DEFAULT 0,
    thinking_mode TEXT DEFAULT '',
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

	// Warn on unexpected version numbers.
	// The migration sequence is: 1→2→3→4→5→6→8→9→10→11→12→13→14→15→16 (v7 never existed).
	// If the stored version is an impossible value (e.g., 7 or > schemaVersion),
	// log a warning to aid debugging, but still proceed with applicable migrations.
	if from == 7 {
		log.WithField("from_version", from).Warn("Schema version 7 never existed; possible manual version corruption. Proceeding with migrations.")
	}
	if from > schemaVersion {
		log.WithFields(log.Fields{
			"from_version":   from,
			"schema_version": schemaVersion,
		}).Warn("Stored schema version exceeds expected; database may be from a newer build")
	}

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

	if from < 8 {
		// Migration to add user_id to core_memory_blocks for per-user human block
		// SQLite's ALTER TABLE ADD COLUMN doesn't modify existing PRIMARY KEY.
		// Must recreate table to update PRIMARY KEY from (tenant_id, block_name) to (tenant_id, block_name, user_id).

		// Step 1: Create new table with correct PRIMARY KEY
		_, err := conn.Exec(`
			CREATE TABLE IF NOT EXISTS core_memory_blocks_new (
				tenant_id INTEGER NOT NULL,
				block_name TEXT NOT NULL,
				user_id TEXT NOT NULL DEFAULT '',
				content TEXT NOT NULL DEFAULT '',
				char_limit INTEGER NOT NULL DEFAULT 2000,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (tenant_id, block_name, user_id),
				FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
			)
		`)
		if err != nil {
			return fmt.Errorf("migrate v6->v8: create new table: %w", err)
		}

		// Step 2: Copy data from old table (user_id defaults to '' for existing rows)
		_, err = conn.Exec(`
			INSERT INTO core_memory_blocks_new (tenant_id, block_name, user_id, content, char_limit, updated_at)
			SELECT tenant_id, block_name, '', content, char_limit, updated_at
			FROM core_memory_blocks
		`)
		if err != nil {
			return fmt.Errorf("migrate v6->v8: copy data: %w", err)
		}

		// Step 3: Drop old table
		_, err = conn.Exec("DROP TABLE core_memory_blocks")
		if err != nil {
			return fmt.Errorf("migrate v6->v8: drop old table: %w", err)
		}

		// Step 4: Rename new table to original name
		_, err = conn.Exec("ALTER TABLE core_memory_blocks_new RENAME TO core_memory_blocks")
		if err != nil {
			return fmt.Errorf("migrate v6->v8: rename table: %w", err)
		}

		log.Info("Database migrated to v8 (added user_id with correct PRIMARY KEY to core_memory_blocks)")

		// Update schema version
		if _, err := conn.Exec("UPDATE schema_version SET version = 8"); err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
	}

	if from < 9 {
		// v9: Fix incorrect PRIMARY KEY from buggy v6->v8 migration
		// The buggy migration (commit 92403ae) added user_id column but didn't update PRIMARY KEY.
		// This caused PRIMARY KEY to remain (tenant_id, block_name) instead of (tenant_id, block_name, user_id).
		// Result: per-user human blocks were overwriting each other.

		// Check if PRIMARY KEY is correct by inspecting pragma_table_info
		var pkCount int
		err := conn.QueryRow(`
			SELECT COUNT(*) FROM pragma_table_info('core_memory_blocks') WHERE pk > 0
		`).Scan(&pkCount)
		if err != nil {
			return fmt.Errorf("migrate v8->v9: check primary key: %w", err)
		}

		// If pkCount is 2, PRIMARY KEY is wrong (tenant_id, block_name)
		// If pkCount is 3, PRIMARY KEY is correct (tenant_id, block_name, user_id)
		if pkCount == 2 {
			log.Warn("Detected incorrect PRIMARY KEY (2 columns), rebuilding core_memory_blocks table...")

			// Step 1: Create new table with correct PRIMARY KEY
			_, err = conn.Exec(`
				CREATE TABLE core_memory_blocks_new (
					tenant_id INTEGER NOT NULL,
					block_name TEXT NOT NULL,
					user_id TEXT NOT NULL DEFAULT '',
					content TEXT NOT NULL DEFAULT '',
					char_limit INTEGER NOT NULL DEFAULT 2000,
					updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					PRIMARY KEY (tenant_id, block_name, user_id),
					FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
				)
			`)
			if err != nil {
				return fmt.Errorf("migrate v8->v9: create new table: %w", err)
			}

			// Step 2: Copy existing data (user_id may already exist or default to '')
			_, err = conn.Exec(`
				INSERT INTO core_memory_blocks_new (tenant_id, block_name, user_id, content, char_limit, updated_at)
				SELECT tenant_id, block_name, COALESCE(user_id, ''), content, char_limit, updated_at
				FROM core_memory_blocks
			`)
			if err != nil {
				return fmt.Errorf("migrate v8->v9: copy data: %w", err)
			}

			// Step 3: Drop old table
			_, err = conn.Exec("DROP TABLE core_memory_blocks")
			if err != nil {
				return fmt.Errorf("migrate v8->v9: drop old table: %w", err)
			}

			// Step 4: Rename new table
			_, err = conn.Exec("ALTER TABLE core_memory_blocks_new RENAME TO core_memory_blocks")
			if err != nil {
				return fmt.Errorf("migrate v8->v9: rename table: %w", err)
			}

			log.Info("Database migrated to v9 (fixed PRIMARY KEY to include user_id)")
		} else {
			log.WithField("pk_count", pkCount).Info("PRIMARY KEY already correct, skipping v9 rebuild")
		}

		// Update schema version
		if _, err := conn.Exec("UPDATE schema_version SET version = 9"); err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
	}

	if from < 10 {
		// v10: Add max_context column to user_llm_configs
		var count int
		err := conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('user_llm_configs') WHERE name = 'max_context'").Scan(&count)
		if err == nil && count == 0 {
			_, err = conn.Exec("ALTER TABLE user_llm_configs ADD COLUMN max_context INTEGER DEFAULT 0")
			if err != nil {
				return fmt.Errorf("migrate v9->v10: %w", err)
			}
			log.Info("Database migrated to v10 (added max_context to user_llm_configs)")
		}
		if _, err := conn.Exec("UPDATE schema_version SET version = 10"); err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
	}

	if from < 11 {
		// v11: Add thinking_mode column to user_llm_configs
		var count int
		err := conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('user_llm_configs') WHERE name = 'thinking_mode'").Scan(&count)
		if err == nil && count == 0 {
			_, err = conn.Exec("ALTER TABLE user_llm_configs ADD COLUMN thinking_mode TEXT DEFAULT ''")
			if err != nil {
				return fmt.Errorf("migrate v10->v11: %w", err)
			}
			log.Info("Database migrated to v11 (added thinking_mode to user_llm_configs)")
		}
		if _, err := conn.Exec("UPDATE schema_version SET version = 11"); err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
	}

	if from < 12 {
		// v12: Remove CodeBuddy-specific columns from user_llm_configs
		_, err := conn.Exec(`
			CREATE TABLE user_llm_configs_new (
				sender_id TEXT PRIMARY KEY,
				provider TEXT NOT NULL,
				base_url TEXT NOT NULL,
				api_key TEXT NOT NULL,
				model TEXT,
				max_context INTEGER DEFAULT 0,
				thinking_mode TEXT DEFAULT '',
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			);
		`)
		if err != nil {
			return fmt.Errorf("migrate v11->v12: create new table: %w", err)
		}

		_, err = conn.Exec(`
			INSERT INTO user_llm_configs_new
			(sender_id, provider, base_url, api_key, model, max_context, thinking_mode, created_at, updated_at)
			SELECT sender_id, provider, base_url, api_key, model, COALESCE(max_context, 0), COALESCE(thinking_mode, ''), created_at, updated_at
			FROM user_llm_configs;
		`)
		if err != nil {
			return fmt.Errorf("migrate v11->v12: copy data: %w", err)
		}

		_, err = conn.Exec(`DROP TABLE user_llm_configs;`)
		if err != nil {
			return fmt.Errorf("migrate v11->v12: drop old table: %w", err)
		}

		_, err = conn.Exec(`ALTER TABLE user_llm_configs_new RENAME TO user_llm_configs;`)
		if err != nil {
			return fmt.Errorf("migrate v11->v12: rename table: %w", err)
		}

		if _, err := conn.Exec("UPDATE schema_version SET version = 12"); err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
		log.Info("Database migrated to v12 (removed CodeBuddy columns)")
	}

	if from < 13 {
		migration := `
CREATE TABLE IF NOT EXISTS shared_registry (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL CHECK(type IN ('skill', 'agent')),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    author      TEXT NOT NULL,
    tags        TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL,
    sharing     TEXT NOT NULL DEFAULT 'private' CHECK(sharing IN ('private', 'public')),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_shared_type_sharing ON shared_registry(type, sharing);
CREATE INDEX IF NOT EXISTS idx_shared_author ON shared_registry(author);

CREATE TABLE IF NOT EXISTS user_settings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    channel    TEXT NOT NULL,
    sender_id  TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    UNIQUE(channel, sender_id, key)
);
CREATE INDEX IF NOT EXISTS idx_user_settings_sender ON user_settings(channel, sender_id);

UPDATE schema_version SET version = 13;
`
		if _, err := conn.Exec(migration); err != nil {
			return fmt.Errorf("migrate v12->v13: %w", err)
		}
		log.Info("Database migrated to v13 (added shared_registry, user_settings)")
	}

	if from < 14 {
		// v14: Add UNIQUE(type, name, author) constraint to shared_registry for atomic UPSERT
		_, err := conn.Exec(`
			CREATE TABLE shared_registry_new (
			    id          INTEGER PRIMARY KEY AUTOINCREMENT,
			    type        TEXT NOT NULL CHECK(type IN ('skill', 'agent')),
			    name        TEXT NOT NULL,
			    description TEXT NOT NULL DEFAULT '',
			    author      TEXT NOT NULL,
			    tags        TEXT NOT NULL DEFAULT '',
			    source_path TEXT NOT NULL,
			    sharing     TEXT NOT NULL DEFAULT 'private' CHECK(sharing IN ('private', 'public')),
			    created_at  INTEGER NOT NULL,
			    updated_at  INTEGER NOT NULL,
			    UNIQUE(type, name, author)
			)
		`)
		if err != nil {
			return fmt.Errorf("migrate v13->v14: create new table: %w", err)
		}

		_, err = conn.Exec(`
			INSERT INTO shared_registry_new (id, type, name, description, author, tags, source_path, sharing, created_at, updated_at)
			SELECT id, type, name, description, author, tags, source_path, sharing, created_at, updated_at
			FROM shared_registry
		`)
		if err != nil {
			return fmt.Errorf("migrate v13->v14: copy data: %w", err)
		}

		_, err = conn.Exec("DROP TABLE shared_registry")
		if err != nil {
			return fmt.Errorf("migrate v13->v14: drop old table: %w", err)
		}

		_, err = conn.Exec("ALTER TABLE shared_registry_new RENAME TO shared_registry")
		if err != nil {
			return fmt.Errorf("migrate v13->v14: rename table: %w", err)
		}

		_, err = conn.Exec("CREATE INDEX IF NOT EXISTS idx_shared_type_sharing ON shared_registry(type, sharing)")
		if err != nil {
			return fmt.Errorf("migrate v13->v14: create index: %w", err)
		}

		_, err = conn.Exec("CREATE INDEX IF NOT EXISTS idx_shared_author ON shared_registry(author)")
		if err != nil {
			return fmt.Errorf("migrate v13->v14: create index: %w", err)
		}

		if _, err := conn.Exec("UPDATE schema_version SET version = 14"); err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
		log.Info("Database migrated to v14 (added UNIQUE constraint to shared_registry)")
	}

	if from < 15 {
		migration := `
CREATE TABLE IF NOT EXISTS runner_tokens (
    user_id     TEXT PRIMARY KEY,
    token       TEXT NOT NULL,
    mode        TEXT NOT NULL DEFAULT 'native',
    docker_image TEXT NOT NULL DEFAULT '',
    workspace   TEXT NOT NULL DEFAULT '/workspace',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
`
		if _, err := conn.Exec(migration); err != nil {
			return fmt.Errorf("migrate v14->v15: %w", err)
		}
		if _, err := conn.Exec("UPDATE schema_version SET version = 15"); err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
		log.Info("Database migrated to v15 (added runner_tokens)")
	}

	if from < 16 {
		migration := `
CREATE TABLE IF NOT EXISTS web_users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    username   TEXT NOT NULL UNIQUE,
    password   TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
`
		if _, err := conn.Exec(migration); err != nil {
			return fmt.Errorf("migrate v15->v16: %w", err)
		}
		if _, err := conn.Exec("UPDATE schema_version SET version = 16"); err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
		log.Info("Database migrated to v16 (added web_users)")
	}
	if from < 17 {
		// v17: Add runners table for multi-runner support.
		// Migrate existing runner_tokens data into runners table (one runner per user, name="default").
		migration := `
CREATE TABLE IF NOT EXISTS runners (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    token        TEXT    NOT NULL UNIQUE,
    mode         TEXT    NOT NULL DEFAULT 'native',
    docker_image TEXT    NOT NULL DEFAULT 'ubuntu:22.04',
    workspace    TEXT    NOT NULL DEFAULT '',
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, name)
);
`
		if _, err := conn.Exec(migration); err != nil {
			return fmt.Errorf("migrate v16->v17: %w", err)
		}

		// Migrate existing runner_tokens entries into runners table.
		// Each existing user gets a runner named "default".
		_, err := conn.Exec(`
			INSERT OR IGNORE INTO runners (user_id, name, token, mode, docker_image, workspace, created_at)
			SELECT user_id, 'default', token, mode, docker_image, workspace, created_at
			FROM runner_tokens
		`)
		if err != nil {
			log.WithError(err).Warn("Failed to migrate runner_tokens to runners table")
		}

		// Set active runner for existing users.
		_, err = conn.Exec(`
			INSERT OR IGNORE INTO user_settings (channel, sender_id, key, value, updated_at)
			SELECT 'web', user_id, 'active_runner', 'default', CAST(strftime('%s','now') AS INTEGER)
			FROM runner_tokens
		`)
		if err != nil {
			log.WithError(err).Warn("Failed to set active_runner for migrated users")
		}

		if _, err := conn.Exec("UPDATE schema_version SET version = 17"); err != nil {
			return fmt.Errorf("update schema version: %w", err)
		}
		log.Info("Database migrated to v17 (added runners table, migrated runner_tokens)")
	}

	return nil
	return nil
}
