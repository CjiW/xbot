package sqlite

import (
	"database/sql"
	"fmt"
)

// UserTokenUsage represents a user's cumulative token usage.
type UserTokenUsage struct {
	SenderID       string `json:"sender_id"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	TotalTokens    int64  `json:"total_tokens"`
	ConversationCount int64 `json:"conversation_count"`
	LLMCallCount   int64  `json:"llm_call_count"`
}

// UserTokenUsageService manages per-user token usage persistence.
type UserTokenUsageService struct {
	db *DB
}

// NewUserTokenUsageService creates a new service.
func NewUserTokenUsageService(db *DB) *UserTokenUsageService {
	return &UserTokenUsageService{db: db}
}

// createTable creates the user_token_usage table (called during migration).
func (s *UserTokenUsageService) createTable(conn *sql.DB) error {
	_, err := conn.Exec(`
	CREATE TABLE IF NOT EXISTS user_token_usage (
		sender_id TEXT PRIMARY KEY,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		conversation_count INTEGER NOT NULL DEFAULT 0,
		llm_call_count INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`)
	return err
}

// RecordUsage upserts token usage for a user.
// Uses INSERT OR REPLACE for simplicity (single connection, no concurrency issue).
func (s *UserTokenUsageService) RecordUsage(conn *sql.DB, senderID string, inputTokens, outputTokens int, conversationCount, llmCallCount int) error {
	_, err := conn.Exec(`
		INSERT INTO user_token_usage (sender_id, input_tokens, output_tokens, total_tokens, conversation_count, llm_call_count, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(sender_id) DO UPDATE SET
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			total_tokens = total_tokens + excluded.total_tokens,
			conversation_count = conversation_count + excluded.conversation_count,
			llm_call_count = llm_call_count + excluded.llm_call_count,
			updated_at = CURRENT_TIMESTAMP
	`, senderID, inputTokens, outputTokens, inputTokens+outputTokens, conversationCount, llmCallCount)
	if err != nil {
		return fmt.Errorf("record user token usage: %w", err)
	}
	return nil
}

// GetUsage retrieves cumulative token usage for a user.
func (s *UserTokenUsageService) GetUsage(senderID string) (*UserTokenUsage, error) {
	conn := s.db.Conn()
	row := conn.QueryRow(`
		SELECT sender_id, input_tokens, output_tokens, total_tokens, conversation_count, llm_call_count
		FROM user_token_usage WHERE sender_id = ?
	`, senderID)

	var u UserTokenUsage
	err := row.Scan(&u.SenderID, &u.InputTokens, &u.OutputTokens, &u.TotalTokens, &u.ConversationCount, &u.LLMCallCount)
	if err == sql.ErrNoRows {
		return &UserTokenUsage{SenderID: senderID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user token usage: %w", err)
	}
	return &u, nil
}

// GetAllUsage retrieves token usage for all users, sorted by total_tokens desc.
func (s *UserTokenUsageService) GetAllUsage() ([]UserTokenUsage, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
		SELECT sender_id, input_tokens, output_tokens, total_tokens, conversation_count, llm_call_count
		FROM user_token_usage ORDER BY total_tokens DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("get all user token usage: %w", err)
	}
	defer rows.Close()

	var result []UserTokenUsage
	for rows.Next() {
		var u UserTokenUsage
		if err := rows.Scan(&u.SenderID, &u.InputTokens, &u.OutputTokens, &u.TotalTokens, &u.ConversationCount, &u.LLMCallCount); err != nil {
			return nil, fmt.Errorf("scan user token usage: %w", err)
		}
		result = append(result, u)
	}
	return result, rows.Err()
}
