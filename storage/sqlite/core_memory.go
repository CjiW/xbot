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
func (s *CoreMemoryService) InitBlocks(tenantID int64, userID *int64) error {
	conn := s.db.Conn()
	for name, limit := range DefaultBlocks {
		// human block is per-user, others are per-tenant
		var uid *int64
		if name == "human" && userID != nil {
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
// For human block, if userID is provided, reads the user-specific block.
func (s *CoreMemoryService) GetBlock(tenantID int64, blockName string, userID *int64) (content string, charLimit int, err error) {
	conn := s.db.Conn()

	// For human block, use userID if provided
	var uid *int64
	if blockName == "human" && userID != nil {
		uid = userID
	}

	// Handle NULL comparison for user_id
	var query string
	var args []interface{}
	if uid == nil {
		query = "SELECT content, char_limit FROM core_memory_blocks WHERE tenant_id = ? AND block_name = ? AND user_id IS NULL"
		args = []interface{}{tenantID, blockName}
	} else {
		query = "SELECT content, char_limit FROM core_memory_blocks WHERE tenant_id = ? AND block_name = ? AND user_id = ?"
		args = []interface{}{tenantID, blockName, *uid}
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
func (s *CoreMemoryService) SetBlock(tenantID int64, blockName, content string, userID *int64) error {
	conn := s.db.Conn()

	// For human block, use userID if provided
	var uid *int64
	if blockName == "human" && userID != nil {
		uid = userID
	}

	// Get char limit
	_, charLimit, err := s.GetBlock(tenantID, blockName, uid)
	if err != nil {
		return err
	}
	if len(content) > charLimit {
		return fmt.Errorf("content length %d exceeds block %q char_limit %d", len(content), blockName, charLimit)
	}

	// SQLite ON CONFLICT doesn't handle NULL correctly, so use different strategies
	if uid == nil {
		// For NULL user_id, use check-then-update/insert
		var count int
		err = conn.QueryRow("SELECT COUNT(*) FROM core_memory_blocks WHERE tenant_id = ? AND block_name = ? AND user_id IS NULL",
			tenantID, blockName).Scan(&count)
		if err != nil {
			return fmt.Errorf("check block existence: %w", err)
		}
		if count > 0 {
			_, err = conn.Exec("UPDATE core_memory_blocks SET content = ?, updated_at = CURRENT_TIMESTAMP WHERE tenant_id = ? AND block_name = ? AND user_id IS NULL",
				content, tenantID, blockName)
		} else {
			_, err = conn.Exec("INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit) VALUES (?, ?, NULL, ?, ?)",
				tenantID, blockName, content, charLimit)
		}
	} else {
		_, err = conn.Exec(`
			INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(tenant_id, block_name, user_id)
			DO UPDATE SET content = excluded.content, updated_at = CURRENT_TIMESTAMP
		`, tenantID, blockName, *uid, content, charLimit)
	}
	if err != nil {
		return fmt.Errorf("set block %s: %w", blockName, err)
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
// If userID is provided, includes user-specific human block.
func (s *CoreMemoryService) GetAllBlocks(tenantID int64, userID *int64) (map[string]string, error) {
	conn := s.db.Conn()

	// Query: get global blocks + user-specific human block
	rows, err := conn.Query(`
		SELECT block_name, content FROM core_memory_blocks
		WHERE tenant_id = ?
		  AND (user_id IS NULL OR user_id = ?)
		ORDER BY block_name
	`, tenantID, userID)
	if err != nil {
		return nil, fmt.Errorf("query blocks: %w", err)
	}
	defer rows.Close()

	blocks := make(map[string]string)
	for rows.Next() {
		var name, content string
		if err := rows.Scan(&name, &content); err != nil {
			return nil, fmt.Errorf("scan block: %w", err)
		}
		blocks[name] = content
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blocks: %w", err)
	}
	return blocks, nil
}
