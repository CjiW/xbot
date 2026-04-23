package sqlite

import (
	"fmt"
	"time"

	log "xbot/logger"
)

// TenantService handles tenant CRUD operations
type TenantService struct {
	db *DB
}

// NewTenantService creates a new tenant service
func NewTenantService(db *DB) *TenantService {
	return &TenantService{db: db}
}

// GetOrCreateTenantID retrieves a tenant ID by (channel, chat_id), creating it if it doesn't exist.
// Uses INSERT OR IGNORE within a transaction to avoid TOCTOU race conditions.
// The UNIQUE(channel, chat_id) constraint on the tenants table guarantees uniqueness.
func (s *TenantService) GetOrCreateTenantID(channel, chatID string) (int64, error) {
	conn := s.db.Conn()

	tx, err := conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()

	// INSERT OR IGNORE: if the row already exists (UNIQUE constraint), it is silently skipped.
	_, err = tx.Exec(
		"INSERT OR IGNORE INTO tenants (channel, chat_id, created_at, last_active_at) VALUES (?, ?, ?, ?)",
		channel, chatID, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("insert or ignore tenant: %w", err)
	}

	// SELECT the tenant ID (works for both newly inserted and pre-existing rows).
	var tenantID int64
	err = tx.QueryRow(
		"SELECT id FROM tenants WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	).Scan(&tenantID)
	if err != nil {
		return 0, fmt.Errorf("select tenant: %w", err)
	}

	// Always update last_active_at to reflect current usage.
	if _, err := tx.Exec(
		"UPDATE tenants SET last_active_at = ? WHERE id = ?",
		now, tenantID,
	); err != nil {
		log.WithError(err).Warn("Failed to update tenant last_active_at")
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return tenantID, nil
}

// GetTenantInfo retrieves tenant information by ID
func (s *TenantService) GetTenantInfo(tenantID int64) (channel, chatID string, err error) {
	conn := s.db.Conn()
	err = conn.QueryRow(
		"SELECT channel, chat_id FROM tenants WHERE id = ?",
		tenantID,
	).Scan(&channel, &chatID)
	if err != nil {
		return "", "", fmt.Errorf("query tenant info: %w", err)
	}
	return channel, chatID, nil
}

// DeleteTenant removes a tenant and all associated data (cascade)
func (s *TenantService) DeleteTenant(tenantID int64) error {
	conn := s.db.Conn()
	result, err := conn.Exec("DELETE FROM tenants WHERE id = ?", tenantID)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("tenant not found: %d", tenantID)
	}
	log.WithField("tenant_id", tenantID).Info("Tenant deleted")
	return nil
}

// GetTenantIDByChannelChatID looks up the tenant ID for (channel, chatID) without creating one.
// Returns (0, nil) if not found.
func (s *TenantService) GetTenantIDByChannelChatID(channel, chatID string) (int64, error) {
	conn := s.db.Conn()
	var tenantID int64
	err := conn.QueryRow(
		"SELECT id FROM tenants WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	).Scan(&tenantID)
	if err != nil {
		return 0, nil // not found
	}
	return tenantID, nil
}

// ListTenants returns all tenants
func (s *TenantService) ListTenants() ([]TenantInfo, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(
		"SELECT id, channel, chat_id, created_at, last_active_at FROM tenants ORDER BY last_active_at DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	var tenants []TenantInfo
	for rows.Next() {
		var t TenantInfo
		var createdAt, lastActiveAt string
		if err := rows.Scan(&t.ID, &t.Channel, &t.ChatID, &createdAt, &lastActiveAt); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		t.CreatedAt = parseSQLiteTime(createdAt)
		t.LastActiveAt = parseSQLiteTime(lastActiveAt)
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}
	return tenants, nil
}

// TenantInfo contains tenant information
type TenantInfo struct {
	ID           int64
	Channel      string
	ChatID       string
	CreatedAt    time.Time
	LastActiveAt time.Time
}
