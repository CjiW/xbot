package oauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"xbot/crypto"
	"xbot/storage/sqlite"

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

// SQLiteStorage implements TokenStorage using the shared SQLite database.
type SQLiteStorage struct {
	db *sqlite.DB
}

// NewSQLiteStorage creates a new SQLite-based token storage using the shared DB.
func NewSQLiteStorage(db *sqlite.DB) (*SQLiteStorage, error) {
	storage := &SQLiteStorage{db: db}
	if err := storage.initSchema(); err != nil {
		return nil, fmt.Errorf("initialize OAuth token schema: %w", err)
	}
	log.Info("OAuth token storage initialized (shared DB)")
	return storage, nil
}

// initSchema creates the oauth_tokens table if it doesn't exist.
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
	_, err := s.db.Conn().Exec(query)
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

	err := s.db.Conn().QueryRowContext(ctx, query, provider, channel, chatID).Scan(
		&accessToken, &refreshToken, &expiresAt, &scopesJSON, &rawJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query token: %w", err)
	}

	// 解密 access_token
	if accessToken != "" {
		if decrypted, err := crypto.Decrypt(accessToken); err == nil {
			accessToken = decrypted
		} else {
			log.WithError(err).Warn("Failed to decrypt access token, key may have changed")
		}
	}

	// 解密 refresh_token
	if refreshToken != "" {
		if decrypted, err := crypto.Decrypt(refreshToken); err == nil {
			refreshToken = decrypted
		} else {
			log.WithError(err).Warn("Failed to decrypt refresh token, key may have changed")
		}
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

	accessToken := token.AccessToken
	refreshToken := token.RefreshToken

	// 加密 access_token
	if accessToken != "" {
		encrypted, err := crypto.Encrypt(accessToken)
		if err != nil {
			return fmt.Errorf("encrypt access token: %w", err)
		}
		accessToken = encrypted
	}

	// 加密 refresh_token（长期有效凭证，必须加密）
	if refreshToken != "" {
		encrypted, err := crypto.Encrypt(refreshToken)
		if err != nil {
			return fmt.Errorf("encrypt refresh token: %w", err)
		}
		refreshToken = encrypted
	}

	query := `
	REPLACE INTO oauth_tokens (provider, channel, chat_id, access_token, refresh_token, expires_at, scopes, raw, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.Conn().ExecContext(ctx, query,
		provider, channel, chatID,
		accessToken, refreshToken, token.ExpiresAt.Unix(),
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
	_, err := s.db.Conn().ExecContext(ctx, query, provider, channel, chatID)
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	return nil
}

// Close is a no-op since the shared DB is managed externally.
func (s *SQLiteStorage) Close() error {
	return nil
}

// CleanupExpiredTokens removes tokens that have expired.
func (s *SQLiteStorage) CleanupExpiredTokens(ctx context.Context, olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan)
	result, err := s.db.Conn().ExecContext(ctx, `DELETE FROM oauth_tokens WHERE expires_at < ?`, cutoff.Unix())
	if err != nil {
		return fmt.Errorf("cleanup expired tokens: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows > 0 {
		log.WithField("count", rows).Info("Cleaned up expired OAuth tokens")
	}
	return nil
}
