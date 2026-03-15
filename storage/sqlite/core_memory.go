package sqlite

import (
	"database/sql"
	"fmt"

	log "xbot/logger"
)

// CoreMemoryService handles core memory block CRUD operations.
type CoreMemoryService struct {
	db *DB
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
// For human block, uses userID if non-empty (for per-user human block).
func (s *CoreMemoryService) InitBlocks(tenantID int64, userID string) error {
	conn := s.db.Conn()
	for name, limit := range DefaultBlocks {
		// human block is per-user, others are per-tenant (userID="")
		uid := ""
		if name == "human" && userID != "" {
			uid = userID
		}
		_, err := conn.Exec(`
			INSERT OR IGNORE INTO core_memory_blocks (tenant_id, block_name, user_id, char_limit)
			VALUES (?, ?, ?, ?)
		`, tenantID, name, uid, limit)
		if err != nil {
			return fmt.Errorf("init block %s: %w", name, err)
		}
	}
	return nil
}

// GetBlock reads a single core memory block.
// - persona: always uses tenantID=1 (global shared)
// - human: uses userID directly as key (cross-tenant shared)
// - working_context: uses tenantID (per-tenant)
func (s *CoreMemoryService) GetBlock(tenantID int64, blockName string, userID string) (content string, charLimit int, err error) {
	conn := s.db.Conn()

	// Resolve effective tenantID and userID based on block type
	effectiveTenantID := tenantID
	uid := ""

	switch blockName {
	case "persona":
		// persona is global, always use tenantID=1
		effectiveTenantID = 1
	case "human":
		// human is per-user, use userID directly (cross-tenant)
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
// - persona: always uses tenantID=1 (global shared)
// - human: uses userID directly as key (cross-tenant shared)
// - working_context: uses tenantID (per-tenant)
func (s *CoreMemoryService) SetBlock(tenantID int64, blockName, content string, userID string) error {
	conn := s.db.Conn()

	// Resolve effective tenantID and userID based on block type
	effectiveTenantID := tenantID
	uid := ""

	switch blockName {
	case "persona":
		// persona is global, always use tenantID=1
		effectiveTenantID = 1
	case "human":
		// human is per-user, use userID directly (cross-tenant)
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
// - persona: from tenantID=1 (global shared)
// - human: from userID directly (cross-tenant shared)
// - working_context: from tenantID (per-tenant)
func (s *CoreMemoryService) GetAllBlocks(tenantID int64, userID string) (map[string]string, error) {
	conn := s.db.Conn()

	blocks := make(map[string]string)

	// Get persona from tenantID=1 (global)
	var personaContent string
	err := conn.QueryRow(
		"SELECT content FROM core_memory_blocks WHERE tenant_id = 1 AND block_name = 'persona' AND user_id = ''",
	).Scan(&personaContent)
	if err == nil {
		blocks["persona"] = personaContent
	}

	// Get working_context from current tenantID
	var wcContent string
	err = conn.QueryRow(
		"SELECT content FROM core_memory_blocks WHERE tenant_id = ? AND block_name = 'working_context' AND user_id = ''",
		tenantID,
	).Scan(&wcContent)
	if err == nil {
		blocks["working_context"] = wcContent
	}

	// Get human from userID directly (cross-tenant)
	if userID != "" {
		var humanContent string
		err := conn.QueryRow(
			"SELECT content FROM core_memory_blocks WHERE block_name = 'human' AND user_id = ?",
			userID,
		).Scan(&humanContent)
		if err == nil {
			blocks["human"] = humanContent
		}
	}

	return blocks, nil
}
