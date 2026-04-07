package sqlite

import (
	"fmt"

	log "xbot/logger"
)

// createSchema creates the initial database schema (v2 baseline).
// After creation, migrateSchema is called to bring it to the current version.
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
    display_only INTEGER DEFAULT 0,
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
INSERT INTO schema_version (version) VALUES (18);

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
    llm_provider TEXT    NOT NULL DEFAULT '',
    llm_api_key  TEXT    NOT NULL DEFAULT '',
    llm_model    TEXT    NOT NULL DEFAULT '',
    llm_base_url TEXT    NOT NULL DEFAULT '',
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

CREATE TABLE event_triggers (
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
CREATE INDEX idx_event_triggers_sender ON event_triggers(sender_id);
CREATE INDEX idx_event_triggers_type ON event_triggers(event_type, enabled);
`
	if _, err := db.Conn().Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	log.Info("Database schema initialized (v2)")
	return nil
}
