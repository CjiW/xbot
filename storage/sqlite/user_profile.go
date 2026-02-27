package sqlite

import (
	"database/sql"
	"fmt"

	log "xbot/logger"
)

// UserProfileService handles per-user profile CRUD (keyed by sender_id, independent of tenants)
type UserProfileService struct {
	db *DB
}

// NewUserProfileService creates a new user profile service
func NewUserProfileService(db *DB) *UserProfileService {
	return &UserProfileService{db: db}
}

// GetProfile retrieves the name and profile for a sender
func (s *UserProfileService) GetProfile(senderID string) (name, profile string, err error) {
	conn := s.db.Conn()
	err = conn.QueryRow(
		"SELECT name, profile FROM user_profiles WHERE sender_id = ?",
		senderID,
	).Scan(&name, &profile)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get user profile: %w", err)
	}
	return name, profile, nil
}

// SaveProfile upserts the user profile
func (s *UserProfileService) SaveProfile(senderID, name, profile string) error {
	conn := s.db.Conn()
	_, err := conn.Exec(`
		INSERT INTO user_profiles (sender_id, name, profile) VALUES (?, ?, ?)
		ON CONFLICT(sender_id) DO UPDATE SET
			name = CASE WHEN excluded.name != '' THEN excluded.name ELSE user_profiles.name END,
			profile = excluded.profile,
			updated_at = CURRENT_TIMESTAMP
	`, senderID, name, profile)
	if err != nil {
		return fmt.Errorf("save user profile: %w", err)
	}
	log.WithField("sender_id", senderID).Debug("User profile updated")
	return nil
}
