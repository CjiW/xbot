package tools

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"time"

	log "xbot/logger"
)

// RunnerTokenSettings holds per-user runner configuration associated with a token.
type RunnerTokenSettings struct {
	Mode        string // "native" or "docker"
	DockerImage string
	Workspace   string
}

// RunnerTokenEntry represents a single per-user runner token.
type RunnerTokenEntry struct {
	Token     string
	UserID    string
	CreatedAt time.Time
	Settings  RunnerTokenSettings
}

// RunnerTokenStore persists per-user runner tokens in SQLite.
// Each user has at most one active token; generating a new one replaces the old.
type RunnerTokenStore struct {
	db *sql.DB
}

// NewRunnerTokenStore creates a token store backed by the given database connection.
func NewRunnerTokenStore(db *sql.DB) *RunnerTokenStore {
	return &RunnerTokenStore{db: db}
}

// Generate creates a new token for the given user, replacing any existing one.
// Returns the new entry.
func (s *RunnerTokenStore) Generate(userID string, settings RunnerTokenSettings) *RunnerTokenEntry {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.WithError(err).Error("Failed to generate random token bytes")
		return nil
	}
	token := base64.RawURLEncoding.EncodeToString(b)

	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO runner_tokens (user_id, token, mode, docker_image, workspace, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			token = excluded.token,
			mode = excluded.mode,
			docker_image = excluded.docker_image,
			workspace = excluded.workspace,
			created_at = excluded.created_at
	`, userID, token, settings.Mode, settings.DockerImage, settings.Workspace, now.Format(time.RFC3339))
	if err != nil {
		log.WithError(err).Error("Failed to store runner token")
		return nil
	}

	return &RunnerTokenEntry{
		Token:     token,
		UserID:    userID,
		CreatedAt: now,
		Settings:  settings,
	}
}

// Validate checks whether the token exists and is owned by the given user.
func (s *RunnerTokenStore) Validate(token, userID string) bool {
	var storedToken string
	err := s.db.QueryRow(
		"SELECT token FROM runner_tokens WHERE user_id = ?", userID,
	).Scan(&storedToken)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(storedToken), []byte(token)) == 1
}

// Get returns the current token entry for a user, or nil if none exists.
func (s *RunnerTokenStore) Get(userID string) *RunnerTokenEntry {
	var token, mode, dockerImage, workspace, createdAtStr string
	err := s.db.QueryRow(
		"SELECT token, mode, docker_image, workspace, created_at FROM runner_tokens WHERE user_id = ?",
		userID,
	).Scan(&token, &mode, &dockerImage, &workspace, &createdAtStr)
	if err != nil {
		return nil
	}
	createdAt, _ := time.Parse(time.RFC3339, createdAtStr)
	return &RunnerTokenEntry{
		Token:     token,
		UserID:    userID,
		CreatedAt: createdAt,
		Settings: RunnerTokenSettings{
			Mode:        mode,
			DockerImage: dockerImage,
			Workspace:   workspace,
		},
	}
}

// Revoke deletes the token for a user.
func (s *RunnerTokenStore) Revoke(userID string) {
	_, err := s.db.Exec("DELETE FROM runner_tokens WHERE user_id = ?", userID)
	if err != nil {
		log.WithError(err).Error("Failed to revoke runner token")
	}
}
