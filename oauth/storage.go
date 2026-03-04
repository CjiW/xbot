package oauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
	log "xbot/logger"
)

// TokenStorage defines the interface for storing OAuth tokens.
type TokenStorage interface {
	// GetToken retrieves a token for a provider and session.
	GetToken(ctx context.Context, provider, channel, chatID string) (*Token, error)

	// SetToken stores a token for a provider and session.
	SetToken(ctx context.Context, provider, channel, chatID string, token *Token) error

	// DeleteToken removes a token (e.g., on user logout).
	DeleteToken(ctx context.Context, provider, channel, chatID string) error

	// Close closes any underlying resources.
	Close() error
}

// SQLiteStorage implements TokenStorage using SQLite.
type SQLiteStorage struct {
	db *sql.DB
}

// NewSQLiteStorage creates a new SQLite-based token storage.
// The database file is created at the specified path if it doesn't exist.
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	// Set pragmatic settings
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	storage := &SQLiteStorage{db: db}

	if err := storage.initSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}

	log.WithField("db", dbPath).Info("OAuth token storage initialized")
	return storage, nil
}

// initSchema creates the necessary tables if they don't exist.
func (s *SQLiteStorage) initSchema() error {
	query := `
	CREATE TABLE IF NOT EXISTS oauth_tokens (
		provider TEXT NOT NULL,
		channel TEXT NOT NULL,
		chat_id TEXT NOT NULL,
		access_token TEXT NOT NULL,
		refresh_token TEXT,
		expires_at INTEGER NOT NULL,
		scopes TEXT,
		raw TEXT,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (provider, channel, chat_id)
	);
	CREATE INDEX IF NOT EXISTS oauth_tokens_expires_at ON oauth_tokens(expires_at);
	`
	_, err := s.db.Exec(query)
	return err
}

// GetToken retrieves a token for a provider and session.
func (s *SQLiteStorage) GetToken(ctx context.Context, provider, channel, chatID string) (*Token, error) {
	query := `
	SELECT access_token, refresh_token, expires_at, scopes, raw
	FROM oauth_tokens
	WHERE provider = ? AND channel = ? AND chat_id = ?
	`

	var accessToken, refreshToken, scopesJSON, rawJSON string
	var expiresAt int64

	err := s.db.QueryRowContext(ctx, query, provider, channel, chatID).Scan(
		&accessToken, &refreshToken, &expiresAt, &scopesJSON, &rawJSON,
	)
	if err == sql.ErrNoRows {
		return nil, nil // No token found, not an error
	}
	if err != nil {
		return nil, fmt.Errorf("query token: %w", err)
	}

	var scopes []string
	if scopesJSON != "" {
		if err := json.Unmarshal([]byte(scopesJSON), &scopes); err != nil {
			log.WithError(err).Warn("Failed to parse scopes JSON")
		}
	}

	var raw map[string]any
	if rawJSON != "" {
		if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
			log.WithError(err).Warn("Failed to parse raw JSON")
		}
	}

	return &Token{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    time.Unix(expiresAt, 0),
		Scopes:       scopes,
		Raw:          raw,
	}, nil
}

// SetToken stores a token for a provider and session.
func (s *SQLiteStorage) SetToken(ctx context.Context, provider, channel, chatID string, token *Token) error {
	scopesJSON, _ := json.Marshal(token.Scopes)
	rawJSON, _ := json.Marshal(token.Raw)

	query := `
	REPLACE INTO oauth_tokens (provider, channel, chat_id, access_token, refresh_token, expires_at, scopes, raw, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		provider, channel, chatID,
		token.AccessToken, token.RefreshToken, token.ExpiresAt.Unix(),
		string(scopesJSON), string(rawJSON), time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("store token: %w", err)
	}

	log.WithFields(log.Fields{
		"provider": provider,
		"channel":  channel,
		"chat_id":  chatID,
	}).Debug("OAuth token stored")
	return nil
}

// DeleteToken removes a token for a provider and session.
func (s *SQLiteStorage) DeleteToken(ctx context.Context, provider, channel, chatID string) error {
	query := `DELETE FROM oauth_tokens WHERE provider = ? AND channel = ? AND chat_id = ?`
	_, err := s.db.ExecContext(ctx, query, provider, channel, chatID)
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	return nil
}

// Close closes the database connection.
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// CleanupExpiredTokens removes tokens that have expired.
// This is a maintenance operation to keep the database clean.
func (s *SQLiteStorage) CleanupExpiredTokens(ctx context.Context, olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan)
	result, err := s.db.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE expires_at < ?`, cutoff.Unix())
	if err != nil {
		return fmt.Errorf("cleanup expired tokens: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows > 0 {
		log.WithField("count", rows).Info("Cleaned up expired OAuth tokens")
	}
	return nil
}
