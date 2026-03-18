package sqlite

import (
	"database/sql"
	"fmt"
	"sync"

	log "xbot/logger"
)

// CoreMemoryService handles core memory block CRUD operations.
type CoreMemoryService struct {
	db           *DB
	migrateOnce  sync.Once
	migrateError error
}

// NewCoreMemoryService creates a new core memory service.
func NewCoreMemoryService(db *DB) *CoreMemoryService {
	return &CoreMemoryService{db: db}
}

// DefaultBlocks are the standard core memory blocks with their character limits.
var DefaultBlocks = map[string]int{
	"persona":         2000, // bot's identity / personality
	"human":           2000, // current user observations
	"working_context": 4000, // active working facts / session context
}

// InitBlocks ensures all default blocks exist for a tenant.
// - persona: uses tenantID (per-tenant, each Agent/SubAgent has independent persona)
// - human: uses userID if non-empty, with tenantID=0 (cross-tenant per-user)
// - working_context: uses tenantID (per-tenant)
func (s *CoreMemoryService) InitBlocks(tenantID int64, userID string) error {
	conn := s.db.Conn()

	// Run migration once (no-op if already done)
	s.migrateOnce.Do(func() {
		s.migrateError = s.migrateLegacyData(conn)
	})
	if s.migrateError != nil {
		return fmt.Errorf("migration: %w", s.migrateError)
	}

	for name, limit := range DefaultBlocks {
		// Determine effective tenantID and userID based on block type
		effectiveTenantID := tenantID
		uid := ""

		switch name {
		case "persona":
			// persona is per-tenant, each Agent/SubAgent has independent persona
			// Use the passed tenantID directly (no override)
		case "human":
			// human is per-user, use userID directly (cross-tenant)
			if userID != "" {
				uid = userID
			}
			// Use tenantID=0 for cross-tenant sharing
			effectiveTenantID = 0
		case "working_context":
			// working_context is per-tenant
			uid = ""
		}

		_, err := conn.Exec(`
			INSERT OR IGNORE INTO core_memory_blocks (tenant_id, block_name, user_id, char_limit)
			VALUES (?, ?, ?, ?)
		`, effectiveTenantID, name, uid, limit)
		if err != nil {
			return fmt.Errorf("init block %s: %w", name, err)
		}
	}
	return nil
}

// migrateLegacyData migrates legacy core memory data to the new scheme:
// - persona: kept at original tenantID (no longer merged to tenantID=0)
// - human: merge all tenantID's human blocks to tenantID=0 (keep longest per user_id)
func (s *CoreMemoryService) migrateLegacyData(db *sql.DB) error {
	// Check if migration marker exists
	var marker int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='core_memory_migrations'").Scan(&marker)
	if err != nil {
		return err
	}

	// Create migration marker table if not exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS core_memory_migrations (
			name TEXT PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}

	// Check if migration already applied
	err = db.QueryRow("SELECT 1 FROM core_memory_migrations WHERE name = 'migrate_to_tenant_0'").Scan(&marker)
	if err == nil {
		// Already migrated
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}

	// NOTE: persona is no longer migrated to tenantID=0
	// Each tenant keeps its own persona at its original tenantID

	// For human: for each user_id, keep longest content from any tenant, insert to tenantID=0
	_, err = db.Exec(`
		INSERT OR REPLACE INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit, updated_at)
		SELECT 0, 'human', user_id, content, char_limit, CURRENT_TIMESTAMP
		FROM (
			SELECT user_id, content, char_limit,
				ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY LENGTH(content) DESC) AS rn
			FROM core_memory_blocks
			WHERE block_name = 'human' AND user_id != ''
		)
		WHERE rn = 1
	`)
	if err != nil {
		return fmt.Errorf("migrate human: %w", err)
	}

	// Clean up legacy human data (old tenantID != 0)
	_, err = db.Exec(`
		DELETE FROM core_memory_blocks
		WHERE block_name = 'human' AND tenant_id != 0
	`)
	if err != nil {
		return fmt.Errorf("cleanup legacy human: %w", err)
	}

	// Mark migration as completed (after successful migration)
	_, err = db.Exec(`
		INSERT INTO core_memory_migrations (name)
		VALUES ('migrate_to_tenant_0')
	`)
	if err != nil {
		return fmt.Errorf("mark migration: %w", err)
	}

	log.Info("Core memory migration completed: human blocks merged to tenantID=0, persona kept at original tenantID")
	return nil
}

// GetBlock reads a single core memory block.
// - persona: uses tenantID (per-tenant, each Agent/SubAgent has independent persona)
// - human: uses userID directly as key (cross-tenant shared)
// - working_context: uses tenantID (per-tenant)
func (s *CoreMemoryService) GetBlock(tenantID int64, blockName string, userID string) (content string, charLimit int, err error) {
	conn := s.db.Conn()

	// Resolve effective tenantID and userID based on block type
	effectiveTenantID := tenantID
	uid := ""

	switch blockName {
	case "persona":
		// persona is per-tenant, use the passed tenantID directly
	case "human":
		// human is per-user, use userID directly (cross-tenant)
		// Use tenantID=0 for cross-tenant sharing
		effectiveTenantID = 0
		if userID != "" {
			uid = userID
		}
	case "working_context":
		// working_context is per-tenant
		uid = ""
	}

	err = conn.QueryRow(
		"SELECT content, char_limit FROM core_memory_blocks WHERE tenant_id = ? AND block_name = ? AND user_id = ?",
		effectiveTenantID, blockName, uid,
	).Scan(&content, &charLimit)
	if err == sql.ErrNoRows {
		// Return defaults for known blocks
		if limit, ok := DefaultBlocks[blockName]; ok {
			return "", limit, nil
		}
		return "", 2000, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("get block %s: %w", blockName, err)
	}
	return content, charLimit, nil
}

// SetBlock upserts a core memory block.
// - persona: uses tenantID (per-tenant, each Agent/SubAgent has independent persona)
// - human: uses userID directly as key (cross-tenant shared)
// - working_context: uses tenantID (per-tenant)
func (s *CoreMemoryService) SetBlock(tenantID int64, blockName, content string, userID string) error {
	conn := s.db.Conn()

	// Resolve effective tenantID and userID based on block type
	effectiveTenantID := tenantID
	uid := ""

	switch blockName {
	case "persona":
		// persona is per-tenant, use the passed tenantID directly
	case "human":
		// human is per-user, use userID directly (cross-tenant)
		effectiveTenantID = 0
		if userID != "" {
			uid = userID
		}
	case "working_context":
		// working_context is per-tenant
		uid = ""
	}

	// Get char limit
	_, charLimit, err := s.GetBlock(effectiveTenantID, blockName, uid)
	if err != nil {
		return err
	}
	if len(content) > charLimit {
		return fmt.Errorf("content length %d exceeds block %q char_limit %d", len(content), blockName, charLimit)
	}

	// user_id is TEXT NOT NULL DEFAULT '', so ON CONFLICT works correctly
	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, block_name, user_id)
		DO UPDATE SET content = excluded.content, updated_at = CURRENT_TIMESTAMP
	`, effectiveTenantID, blockName, uid, content, charLimit)
	if err != nil {
		return fmt.Errorf("set block %s: %w", blockName, err)
	}

	log.WithFields(log.Fields{
		"tenant_id":  effectiveTenantID,
		"block_name": blockName,
		"user_id":    uid,
		"length":     len(content),
	}).Debug("Core memory block updated")
	return nil
}

// GetAllBlocks reads all core memory blocks.
// - persona: from tenantID (per-tenant, each Agent/SubAgent has independent persona)
// - human: from tenantID=0 with userID (cross-tenant per-user)
// - working_context: from tenantID (per-tenant)
func (s *CoreMemoryService) GetAllBlocks(tenantID int64, userID string) (map[string]string, error) {
	conn := s.db.Conn()

	blocks := make(map[string]string)

	// Get persona from current tenantID (per-tenant)
	var personaContent string
	err := conn.QueryRow(
		"SELECT content FROM core_memory_blocks WHERE tenant_id = ? AND block_name = 'persona' AND user_id = ''",
		tenantID,
	).Scan(&personaContent)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("query persona: %w", err)
	}
	if err == nil {
		blocks["persona"] = personaContent
	}

	// Get working_context from current tenantID
	var wcContent string
	err = conn.QueryRow(
		"SELECT content FROM core_memory_blocks WHERE tenant_id = ? AND block_name = 'working_context' AND user_id = ''",
		tenantID,
	).Scan(&wcContent)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("query working_context: %w", err)
	}
	if err == nil {
		blocks["working_context"] = wcContent
	}

	// Get human from tenantID=0 with userID (cross-tenant)
	if userID != "" {
		var humanContent string
		err := conn.QueryRow(
			"SELECT content FROM core_memory_blocks WHERE tenant_id = 0 AND block_name = 'human' AND user_id = ?",
			userID,
		).Scan(&humanContent)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("query human: %w", err)
		}
		if err == nil {
			blocks["human"] = humanContent
		}
	}

	return blocks, nil
}
