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
// For human block, uses userID if provided (for per-user human block).
func (s *CoreMemoryService) InitBlocks(tenantID int64, userID *string) error {
	conn := s.db.Conn()
	for name, limit := range DefaultBlocks {
		// human block is per-user, others are per-tenant
		var uid string
		if name == "human" && userID != nil {
			uid = *userID
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
// For human block, if userID is provided, reads the user-specific block.
func (s *CoreMemoryService) GetBlock(tenantID int64, blockName string, userID *string) (content string, charLimit int, err error) {
	conn := s.db.Conn()

	// For human block, use userID if provided
	var uid string
	if blockName == "human" && userID != nil {
		uid = *userID
	}

	var query string
	var args []interface{}
	if uid == "" {
		// Query for global block (user_id = '')
		query = "SELECT content, char_limit FROM core_memory_blocks WHERE tenant_id = ? AND block_name = ? AND user_id = ''"
		args = []interface{}{tenantID, blockName}
	} else {
		// Query for user-specific block
		query = "SELECT content, char_limit FROM core_memory_blocks WHERE tenant_id = ? AND block_name = ? AND user_id = ?"
		args = []interface{}{tenantID, blockName, uid}
	}

	err = conn.QueryRow(query, args...).Scan(&content, &charLimit)
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
// For human block, if userID is provided, writes to the user-specific block.
func (s *CoreMemoryService) SetBlock(tenantID int64, blockName, content string, userID *string) error {
	conn := s.db.Conn()

	// For human block, use userID if provided
	var uid string
	if blockName == "human" && userID != nil {
		uid = *userID
	}

	// Get char limit
	_, charLimit, err := s.GetBlock(tenantID, blockName, userID)
	if err != nil {
		return err
	}
	if len(content) > charLimit {
		return fmt.Errorf("content length %d exceeds block %q char_limit %d", len(content), blockName, charLimit)
	}

	// Use UPDATE + INSERT pattern
	var result sql.Result
	if uid == "" {
		// Try UPDATE first for global blocks
		result, err = conn.Exec(`
			UPDATE core_memory_blocks SET content = ?, updated_at = CURRENT_TIMESTAMP
			WHERE tenant_id = ? AND block_name = ? AND user_id = ''
		`, content, tenantID, blockName)
	} else {
		result, err = conn.Exec(`
			UPDATE core_memory_blocks SET content = ?, updated_at = CURRENT_TIMESTAMP
			WHERE tenant_id = ? AND block_name = ? AND user_id = ?
		`, content, tenantID, blockName, uid)
	}
	if err != nil {
		return fmt.Errorf("set block %s (update): %w", blockName, err)
	}

	// If no rows affected, INSERT
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set block %s (rows): %w", blockName, err)
	}
	if rowsAffected == 0 {
		_, err = conn.Exec(`
			INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
			VALUES (?, ?, ?, ?, ?)
		`, tenantID, blockName, uid, content, charLimit)
		if err != nil {
			return fmt.Errorf("set block %s (insert): %w", blockName, err)
		}
	}

	log.WithFields(log.Fields{
		"tenant_id":  tenantID,
		"block_name": blockName,
		"user_id":    uid,
		"length":     len(content),
	}).Debug("Core memory block updated")
	return nil
}

// GetAllBlocks reads all core memory blocks for a tenant.
// If userID is provided, includes user-specific human block (takes priority over global).
func (s *CoreMemoryService) GetAllBlocks(tenantID int64, userID *string) (map[string]string, error) {
	conn := s.db.Conn()

	// Query: get all blocks for this tenant
	// If userID is provided, user-specific human block will be returned alongside global blocks
	// We need to handle priority in code (user block takes precedence)
	rows, err := conn.Query(`
		SELECT block_name, content, user_id FROM core_memory_blocks
		WHERE tenant_id = ?
		  AND (user_id = '' OR user_id = ?)
		ORDER BY block_name, user_id
	`, tenantID, userID)
	if err != nil {
		return nil, fmt.Errorf("query blocks: %w", err)
	}
	defer rows.Close()

	blocks := make(map[string]string)
	var userBlock string
	for rows.Next() {
		var name, content, uid string
		if err := rows.Scan(&name, &content, &uid); err != nil {
			return nil, fmt.Errorf("scan block: %w", err)
		}
		// For human block: if this is a user-specific block, store separately
		// Otherwise (global), use directly
		if name == "human" && uid != "" {
			userBlock = content
		} else {
			blocks[name] = content
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blocks: %w", err)
	}

	// User-specific human block takes priority
	if userBlock != "" {
		blocks["human"] = userBlock
	}

	return blocks, nil
}
