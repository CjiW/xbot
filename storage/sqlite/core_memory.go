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
func (s *CoreMemoryService) InitBlocks(tenantID int64) error {
	conn := s.db.Conn()
	for name, limit := range DefaultBlocks {
		_, err := conn.Exec(`
			INSERT OR IGNORE INTO core_memory_blocks (tenant_id, block_name, char_limit)
			VALUES (?, ?, ?)
		`, tenantID, name, limit)
		if err != nil {
			return fmt.Errorf("init block %s: %w", name, err)
		}
	}
	return nil
}

// GetBlock reads a single core memory block.
func (s *CoreMemoryService) GetBlock(tenantID int64, blockName string) (content string, charLimit int, err error) {
	conn := s.db.Conn()
	err = conn.QueryRow(
		"SELECT content, char_limit FROM core_memory_blocks WHERE tenant_id = ? AND block_name = ?",
		tenantID, blockName,
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
func (s *CoreMemoryService) SetBlock(tenantID int64, blockName, content string) error {
	conn := s.db.Conn()

	// Get char limit
	_, charLimit, err := s.GetBlock(tenantID, blockName)
	if err != nil {
		return err
	}
	if len(content) > charLimit {
		return fmt.Errorf("content length %d exceeds block %q char_limit %d", len(content), blockName, charLimit)
	}

	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks (tenant_id, block_name, content, char_limit)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(tenant_id, block_name)
		DO UPDATE SET content = excluded.content, updated_at = CURRENT_TIMESTAMP
	`, tenantID, blockName, content, charLimit)
	if err != nil {
		return fmt.Errorf("set block %s: %w", blockName, err)
	}

	log.WithFields(log.Fields{
		"tenant_id":  tenantID,
		"block_name": blockName,
		"length":     len(content),
	}).Debug("Core memory block updated")
	return nil
}

// GetAllBlocks reads all core memory blocks for a tenant.
func (s *CoreMemoryService) GetAllBlocks(tenantID int64) (map[string]string, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(
		"SELECT block_name, content FROM core_memory_blocks WHERE tenant_id = ? ORDER BY block_name",
		tenantID,
	)
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
