package sqlite

import (
	"database/sql"
	"fmt"
	"strings"

	log "xbot/logger"
)

// migrateSchema runs all pending migrations from the given version.
// The migration sequence is: 1→2→3→4→5→6→8→9→10→11→12→13→14→15→16→17→18→19→20→21
// (v7 never existed).
func (db *DB) migrateSchema(from int) error {
	conn := db.Conn()

	// Warn on unexpected version numbers.
	if from == 7 {
		log.WithField("from_version", from).Warn("Schema version 7 never existed; possible manual version corruption. Proceeding with migrations.")
	}
	if from > schemaVersion {
		log.WithFields(log.Fields{
			"from_version":   from,
			"schema_version": schemaVersion,
		}).Warn("Stored schema version exceeds expected; database may be from a newer build")
	}

	type migration struct {
		version int
		fn      func(conn *sql.DB) error
	}

	// Standard migrations that only need *sql.DB.
	standardMigrations := []migration{
		{2, migrateV1ToV2},
		{3, migrateV2ToV3},
		{4, migrateV3ToV4},
		{5, migrateV4ToV5},
		{6, migrateV5ToV6},
		{8, migrateV6ToV8},
		{9, migrateV8ToV9},
		{10, migrateV9ToV10},
		{11, migrateV10ToV11},
		{12, migrateV11ToV12},
		{13, migrateV12ToV13},
		{14, migrateV13ToV14},
		{15, migrateV14ToV15},
		{16, migrateV15ToV16},
		{17, migrateV16ToV17},
		{18, migrateV17ToV18},
	}

	for _, m := range standardMigrations {
		if from < m.version {
			if err := m.fn(conn); err != nil {
				return fmt.Errorf("migrate to v%d: %w", m.version, err)
			}
		}
	}

	// v19 requires *DB to instantiate UserTokenUsageService.
	if from < 19 {
		if err := migrateV18ToV19WithDB(db); err != nil {
			return fmt.Errorf("migrate to v19: %w", err)
		}
	}

	// Remaining standard migrations.
	lateMigrations := []migration{
		{20, migrateV19ToV20},
		{21, migrateV20ToV21},
		{22, migrateV21ToV22},
	}

	for _, m := range lateMigrations {
		if from < m.version {
			if err := m.fn(conn); err != nil {
				return fmt.Errorf("migrate to v%d: %w", m.version, err)
			}
		}
	}

	return nil
}

// migrateV1ToV2 adds the user_profiles table.
func migrateV1ToV2(conn *sql.DB) error {
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
	return nil
}

// migrateV2ToV3 adds core_memory_blocks, archival_memory, and event_history_fts.
func migrateV2ToV3(conn *sql.DB) error {
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
	return nil
}

// migrateV3ToV4 adds the cron_jobs table.
func migrateV3ToV4(conn *sql.DB) error {
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
	return nil
}

// migrateV4ToV5 adds last_trigger column to cron_jobs.
func migrateV4ToV5(conn *sql.DB) error {
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
	log.Info("Database migrated to v5")
	return nil
}

// migrateV5ToV6 adds the user_llm_configs table.
func migrateV5ToV6(conn *sql.DB) error {
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
	return nil
}

// migrateV6ToV8 adds user_id to core_memory_blocks with correct PRIMARY KEY.
// SQLite's ALTER TABLE ADD COLUMN doesn't modify existing PRIMARY KEY.
// Must recreate table to update PRIMARY KEY from (tenant_id, block_name) to (tenant_id, block_name, user_id).
func migrateV6ToV8(conn *sql.DB) error {
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
	return nil
}

// migrateV8ToV9 fixes incorrect PRIMARY KEY from buggy v6->v8 migration.
// The buggy migration added user_id column but didn't update PRIMARY KEY.
// This caused PRIMARY KEY to remain (tenant_id, block_name) instead of (tenant_id, block_name, user_id).
func migrateV8ToV9(conn *sql.DB) error {
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
	return nil
}

// migrateV9ToV10 adds max_context column to user_llm_configs.
func migrateV9ToV10(conn *sql.DB) error {
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
	return nil
}

// migrateV10ToV11 adds thinking_mode column to user_llm_configs.
func migrateV10ToV11(conn *sql.DB) error {
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
	return nil
}

// migrateV11ToV12 removes CodeBuddy-specific columns from user_llm_configs.
func migrateV11ToV12(conn *sql.DB) error {
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
	return nil
}

// migrateV12ToV13 adds shared_registry and user_settings tables.
func migrateV12ToV13(conn *sql.DB) error {
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
	return nil
}

// migrateV13ToV14 adds UNIQUE(type, name, author) constraint to shared_registry.
func migrateV13ToV14(conn *sql.DB) error {
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
	return nil
}

// migrateV14ToV15 adds the runner_tokens table.
func migrateV14ToV15(conn *sql.DB) error {
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
	return nil
}

// migrateV15ToV16 adds the web_users table.
func migrateV15ToV16(conn *sql.DB) error {
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
	return nil
}

// migrateV16ToV17 adds the runners table and migrates existing runner_tokens data.
func migrateV16ToV17(conn *sql.DB) error {
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
	return nil
}

// migrateV17ToV18 adds display_only column to session_messages.
func migrateV17ToV18(conn *sql.DB) error {
	var count int
	err := conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('session_messages') WHERE name = 'display_only'").Scan(&count)
	if err == nil && count == 0 {
		_, err = conn.Exec("ALTER TABLE session_messages ADD COLUMN display_only INTEGER DEFAULT 0")
		if err != nil {
			return fmt.Errorf("migrate v17->v18: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 18"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v18 (added display_only to session_messages)")
	return nil
}

// migrateV18ToV19WithDB adds the user_token_usage table via UserTokenUsageService.
// This migration requires *DB rather than just *sql.DB because it instantiates a service.
func migrateV18ToV19WithDB(db *DB) error {
	svc := NewUserTokenUsageService(db)
	if err := svc.createTable(db.Conn()); err != nil {
		return fmt.Errorf("migrate v18->v19: %w", err)
	}
	if _, err := db.Conn().Exec("UPDATE schema_version SET version = 19"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v19 (added user_token_usage)")
	return nil
}

// migrateV19ToV20 adds token tracking fields to tenant_state.
func migrateV19ToV20(conn *sql.DB) error {
	if _, err := conn.Exec("ALTER TABLE tenant_state ADD COLUMN last_prompt_tokens INTEGER DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate v19->v20: %w", err)
	}
	if _, err := conn.Exec("ALTER TABLE tenant_state ADD COLUMN last_completion_tokens INTEGER DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate v19->v20: %w", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 20"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v20 (added token tracking to tenant_state)")
	return nil
}

// migrateV20ToV21 adds LLM fields to runners table.
func migrateV20ToV21(conn *sql.DB) error {
	if _, err := conn.Exec("ALTER TABLE runners ADD COLUMN llm_provider TEXT NOT NULL DEFAULT ''"); err != nil {
		// Column may already exist in fresh DB (created with v21+ schema).
		// Skip if error is "duplicate column name".
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v20->v21: %w", err)
		}
	}
	if _, err := conn.Exec("ALTER TABLE runners ADD COLUMN llm_api_key TEXT NOT NULL DEFAULT ''"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v20->v21: %w", err)
		}
	}
	if _, err := conn.Exec("ALTER TABLE runners ADD COLUMN llm_model TEXT NOT NULL DEFAULT ''"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v20->v21: %w", err)
		}
	}
	if _, err := conn.Exec("ALTER TABLE runners ADD COLUMN llm_base_url TEXT NOT NULL DEFAULT ''"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v20->v21: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 21"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v21 (added LLM fields to runners)")
	return nil
}

// migrateV21ToV22 adds the event_triggers table.
func migrateV21ToV22(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS event_triggers (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    event_type  TEXT NOT NULL DEFAULT 'webhook',
    channel     TEXT NOT NULL,
    chat_id     TEXT NOT NULL,
    sender_id   TEXT NOT NULL,
    message_tpl TEXT NOT NULL,
    secret      TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    one_shot    INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    last_fired  TEXT,
    fire_count  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_event_triggers_sender ON event_triggers(sender_id);
CREATE INDEX IF NOT EXISTS idx_event_triggers_type ON event_triggers(event_type, enabled);
UPDATE schema_version SET version = 22;
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v21->v22: %w", err)
	}
	log.Info("Database migrated to v22 (added event_triggers)")
	return nil
}
