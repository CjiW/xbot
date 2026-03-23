package sqlite

import (
	"fmt"
	"time"
)

// UserSettingsService manages per-user settings stored in the user_settings table.
type UserSettingsService struct {
	db *DB
}

// NewUserSettingsService creates a new UserSettingsService.
func NewUserSettingsService(db *DB) *UserSettingsService {
	return &UserSettingsService{db: db}
}

// Get retrieves all settings for a given channel and sender.
func (s *UserSettingsService) Get(channel, senderID string) (map[string]string, error) {
	if s.db == nil {
		return nil, fmt.Errorf("user settings store: database not initialized")
	}
	rows, err := s.db.Conn().Query(
		"SELECT key, value FROM user_settings WHERE channel = ? AND sender_id = ?",
		channel, senderID,
	)
	if err != nil {
		return nil, fmt.Errorf("get user settings: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, rows.Err()
}

// Set creates or updates a single setting.
func (s *UserSettingsService) Set(channel, senderID, key, value string) error {
	if s.db == nil {
		return fmt.Errorf("user settings store: database not initialized")
	}
	now := time.Now().UnixMilli()
	_, err := s.db.Conn().Exec(
		`INSERT INTO user_settings (channel, sender_id, key, value, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(channel, sender_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		channel, senderID, key, value, now,
	)
	if err != nil {
		return fmt.Errorf("set user setting: %w", err)
	}
	return nil
}

// Delete removes a single setting.
func (s *UserSettingsService) Delete(channel, senderID, key string) error {
	if s.db == nil {
		return fmt.Errorf("user settings store: database not initialized")
	}
	_, err := s.db.Conn().Exec(
		"DELETE FROM user_settings WHERE channel = ? AND sender_id = ? AND key = ?",
		channel, senderID, key,
	)
	if err != nil {
		return fmt.Errorf("delete user setting: %w", err)
	}
	return nil
}
