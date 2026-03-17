package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	log "xbot/logger"
)

// MemoryService handles long-term memory and event history operations
type MemoryService struct {
	db *DB
}

// NewMemoryService creates a new memory service
func NewMemoryService(db *DB) *MemoryService {
	return &MemoryService{db: db}
}

// ReadLongTerm retrieves the long-term memory content for a tenant.
// TODO(P2-14): Use ctx for query context propagation instead of context.Background().
func (s *MemoryService) ReadLongTerm(ctx context.Context, tenantID int64) (string, error) {
	conn := s.db.Conn()
	var content sql.NullString
	err := conn.QueryRowContext(ctx,
		"SELECT content FROM long_term_memory WHERE tenant_id = ?",
		tenantID,
	).Scan(&content)
	if err == sql.ErrNoRows {
		// No memory yet, return empty string
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read long-term memory: %w", err)
	}
	if !content.Valid {
		return "", nil
	}
	return content.String, nil
}

// WriteLongTerm saves or updates the long-term memory content for a tenant.
// TODO(P2-14): Use ctx for query context propagation instead of context.Background().
func (s *MemoryService) WriteLongTerm(ctx context.Context, tenantID int64, content string) error {
	conn := s.db.Conn()
	_, err := conn.ExecContext(ctx, `
		INSERT INTO long_term_memory (tenant_id, content) VALUES (?, ?)
		ON CONFLICT(tenant_id) DO UPDATE SET content = excluded.content, updated_at = CURRENT_TIMESTAMP
	`, tenantID, content)
	if err != nil {
		return fmt.Errorf("write long-term memory: %w", err)
	}
	log.WithField("tenant_id", tenantID).Debug("Long-term memory updated")
	return nil
}

// AppendHistory adds an entry to the event history for a tenant.
// TODO(P2-14): Use ctx for query context propagation instead of context.Background().
func (s *MemoryService) AppendHistory(ctx context.Context, tenantID int64, entry string) error {
	conn := s.db.Conn()
	_, err := conn.ExecContext(ctx,
		"INSERT INTO event_history (tenant_id, entry) VALUES (?, ?)",
		tenantID, entry,
	)
	if err != nil {
		return fmt.Errorf("append event history: %w", err)
	}
	log.WithField("tenant_id", tenantID).Debug("Event history entry appended")
	return nil
}

// GetState retrieves the consolidation state for a tenant.
// TODO(P2-14): Use ctx for query context propagation instead of context.Background().
func (s *MemoryService) GetState(ctx context.Context, tenantID int64) (lastConsolidated int, err error) {
	conn := s.db.Conn()
	err = conn.QueryRowContext(ctx,
		"SELECT last_consolidated FROM tenant_state WHERE tenant_id = ?",
		tenantID,
	).Scan(&lastConsolidated)
	if err == sql.ErrNoRows {
		// No state yet, initialize to 0
		if err := s.SetState(ctx, tenantID, 0); err != nil {
			return 0, fmt.Errorf("initialize tenant state: %w", err)
		}
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get tenant state: %w", err)
	}
	return lastConsolidated, nil
}

// SetState updates the consolidation state for a tenant.
// TODO(P2-14): Use ctx for query context propagation instead of context.Background().
func (s *MemoryService) SetState(ctx context.Context, tenantID int64, lastConsolidated int) error {
	conn := s.db.Conn()
	_, err := conn.ExecContext(ctx, `
		INSERT INTO tenant_state (tenant_id, last_consolidated) VALUES (?, ?)
		ON CONFLICT(tenant_id) DO UPDATE SET last_consolidated = excluded.last_consolidated
	`, tenantID, lastConsolidated)
	if err != nil {
		return fmt.Errorf("set tenant state: %w", err)
	}
	return nil
}

// GetHistoryEntries retrieves recent history entries for a tenant.
// TODO(P2-14): Use ctx for query context propagation instead of context.Background().
func (s *MemoryService) GetHistoryEntries(ctx context.Context, tenantID int64, limit int) ([]string, error) {
	conn := s.db.Conn()
	rows, err := conn.QueryContext(ctx, `
		SELECT entry FROM event_history
		WHERE tenant_id = ?
		ORDER BY id DESC
		LIMIT ?
	`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("query event history: %w", err)
	}
	defer rows.Close()

	var entries []string
	for rows.Next() {
		var entry string
		if err := rows.Scan(&entry); err != nil {
			return nil, fmt.Errorf("scan history entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history entries: %w", err)
	}
	return entries, nil
}
