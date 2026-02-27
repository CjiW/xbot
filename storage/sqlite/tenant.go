package sqlite

import (
	"database/sql"
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

// GetOrCreateTenantID retrieves a tenant ID by (channel, chat_id), creating it if it doesn't exist
func (s *TenantService) GetOrCreateTenantID(channel, chatID string) (int64, error) {
	conn := s.db.Conn()

	// Try to find existing tenant
	var tenantID int64
	err := conn.QueryRow(
		"SELECT id FROM tenants WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	).Scan(&tenantID)

	if err == nil {
		// Found existing tenant, update last_active_at
		if _, err := conn.Exec(
			"UPDATE tenants SET last_active_at = ? WHERE id = ?",
			time.Now(), tenantID,
		); err != nil {
			log.WithError(err).Warn("Failed to update tenant last_active_at")
		}
		return tenantID, nil
	}

	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("query tenant: %w", err)
	}

	// Create new tenant
	result, err := conn.Exec(
		"INSERT INTO tenants (channel, chat_id, created_at, last_active_at) VALUES (?, ?, ?, ?)",
		channel, chatID, time.Now(), time.Now(),
	)
	if err != nil {
		return 0, fmt.Errorf("insert tenant: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get last insert id: %w", err)
	}

	log.WithFields(log.Fields{
		"tenant_id": id,
		"channel":   channel,
		"chat_id":   chatID,
	}).Info("New tenant created")

	return id, nil
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
		if err := rows.Scan(&t.ID, &t.Channel, &t.ChatID, &t.CreatedAt, &t.LastActiveAt); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		tenants = append(tenants, t)
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
