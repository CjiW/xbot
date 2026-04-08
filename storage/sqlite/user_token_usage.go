package sqlite

import (
	"database/sql"
	"fmt"
	"time"
)

// UserTokenUsage represents a user's cumulative token usage.
type UserTokenUsage struct {
	SenderID          string `json:"sender_id"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
	CachedTokens      int64  `json:"cached_tokens"`
	ConversationCount int64  `json:"conversation_count"`
	LLMCallCount      int64  `json:"llm_call_count"`
}

// DailyTokenUsage represents token usage for a specific day+model.
type DailyTokenUsage struct {
	Date              string `json:"date"` // YYYY-MM-DD
	SenderID          string `json:"sender_id"`
	Model             string `json:"model"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CachedTokens      int64  `json:"cached_tokens"`
	ConversationCount int64  `json:"conversation_count"`
	LLMCallCount      int64  `json:"llm_call_count"`
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
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		conversation_count INTEGER NOT NULL DEFAULT 0,
		llm_call_count INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`)
	return err
}

// createDailyTable creates the daily_token_usage table (migration v25).
func (s *UserTokenUsageService) createDailyTable(conn *sql.DB) error {
	_, err := conn.Exec(`
	CREATE TABLE IF NOT EXISTS daily_token_usage (
		date TEXT NOT NULL,
		sender_id TEXT NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		conversation_count INTEGER NOT NULL DEFAULT 0,
		llm_call_count INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (date, sender_id, model)
	);
	CREATE INDEX IF NOT EXISTS idx_daily_token_usage_sender ON daily_token_usage(sender_id);
	CREATE INDEX IF NOT EXISTS idx_daily_token_usage_date ON daily_token_usage(date);
	`)
	return err
}

// addCachedTokensColumn adds cached_tokens to existing user_token_usage (migration v25).
func (s *UserTokenUsageService) addCachedTokensColumn(conn *sql.DB) error {
	// Check if column exists; if not, add it
	rows, err := conn.Query("PRAGMA table_info(user_token_usage)")
	if err != nil {
		return err
	}
	defer rows.Close()
	hasCached := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "cached_tokens" {
			hasCached = true
		}
	}
	if !hasCached {
		_, err := conn.Exec("ALTER TABLE user_token_usage ADD COLUMN cached_tokens INTEGER NOT NULL DEFAULT 0")
		return err
	}
	return nil
}

// RecordUsage atomically upserts token usage into both cumulative and daily tables.
// Uses INSERT ... ON CONFLICT DO UPDATE with additive semantics — safe under SQLite WAL
// with busy_timeout even when multiple processes/goroutines write concurrently.
func (s *UserTokenUsageService) RecordUsage(conn *sql.DB, senderID, model string, inputTokens, outputTokens, cachedTokens int, conversationCount, llmCallCount int) error {
	today := time.Now().Format("2006-01-02")
	totalTokens := inputTokens + outputTokens

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx for token usage: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Cumulative per-user usage
	_, err = tx.Exec(`
		INSERT INTO user_token_usage (sender_id, input_tokens, output_tokens, total_tokens, cached_tokens, conversation_count, llm_call_count, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(sender_id) DO UPDATE SET
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			total_tokens = total_tokens + excluded.total_tokens,
			cached_tokens = cached_tokens + excluded.cached_tokens,
			conversation_count = conversation_count + excluded.conversation_count,
			llm_call_count = llm_call_count + excluded.llm_call_count,
			updated_at = CURRENT_TIMESTAMP
	`, senderID, inputTokens, outputTokens, totalTokens, cachedTokens, conversationCount, llmCallCount)
	if err != nil {
		return fmt.Errorf("upsert user_token_usage: %w", err)
	}

	// Daily per-user per-model usage
	_, err = tx.Exec(`
		INSERT INTO daily_token_usage (date, sender_id, model, input_tokens, output_tokens, cached_tokens, conversation_count, llm_call_count, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(date, sender_id, model) DO UPDATE SET
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			cached_tokens = cached_tokens + excluded.cached_tokens,
			conversation_count = conversation_count + excluded.conversation_count,
			llm_call_count = llm_call_count + excluded.llm_call_count,
			updated_at = CURRENT_TIMESTAMP
	`, today, senderID, model, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount)
	if err != nil {
		return fmt.Errorf("upsert daily_token_usage: %w", err)
	}

	return tx.Commit()
}

// GetUsage retrieves cumulative token usage for a user.
func (s *UserTokenUsageService) GetUsage(senderID string) (*UserTokenUsage, error) {
	conn := s.db.Conn()
	row := conn.QueryRow(`
		SELECT sender_id, input_tokens, output_tokens, total_tokens, cached_tokens, conversation_count, llm_call_count
		FROM user_token_usage WHERE sender_id = ?
	`, senderID)

	var u UserTokenUsage
	err := row.Scan(&u.SenderID, &u.InputTokens, &u.OutputTokens, &u.TotalTokens, &u.CachedTokens, &u.ConversationCount, &u.LLMCallCount)
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
		SELECT sender_id, input_tokens, output_tokens, total_tokens, cached_tokens, conversation_count, llm_call_count
		FROM user_token_usage ORDER BY total_tokens DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("get all user token usage: %w", err)
	}
	defer rows.Close()

	var result []UserTokenUsage
	for rows.Next() {
		var u UserTokenUsage
		if err := rows.Scan(&u.SenderID, &u.InputTokens, &u.OutputTokens, &u.TotalTokens, &u.CachedTokens, &u.ConversationCount, &u.LLMCallCount); err != nil {
			return nil, fmt.Errorf("scan user token usage: %w", err)
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

// GetDailyUsage retrieves daily usage for a sender, ordered by date desc.
// If days <= 0, returns all history.
func (s *UserTokenUsageService) GetDailyUsage(senderID string, days int) ([]DailyTokenUsage, error) {
	conn := s.db.Conn()
	var query string
	var args []any
	if days > 0 {
		since := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
		query = `SELECT date, sender_id, model, input_tokens, output_tokens, cached_tokens, conversation_count, llm_call_count
				 FROM daily_token_usage WHERE sender_id = ? AND date >= ? ORDER BY date DESC, model`
		args = []any{senderID, since}
	} else {
		query = `SELECT date, sender_id, model, input_tokens, output_tokens, cached_tokens, conversation_count, llm_call_count
				 FROM daily_token_usage WHERE sender_id = ? ORDER BY date DESC, model`
		args = []any{senderID}
	}
	rows, err := conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get daily token usage: %w", err)
	}
	defer rows.Close()

	var result []DailyTokenUsage
	for rows.Next() {
		var d DailyTokenUsage
		if err := rows.Scan(&d.Date, &d.SenderID, &d.Model, &d.InputTokens, &d.OutputTokens, &d.CachedTokens, &d.ConversationCount, &d.LLMCallCount); err != nil {
			return nil, fmt.Errorf("scan daily token usage: %w", err)
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// GetDailyUsageSummary returns aggregated per-day summaries (across all models).
func (s *UserTokenUsageService) GetDailyUsageSummary(senderID string, days int) ([]DailyTokenUsage, error) {
	conn := s.db.Conn()
	var query string
	var args []any
	if days > 0 {
		since := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
		query = `SELECT date, sender_id, '' as model,
				 SUM(input_tokens), SUM(output_tokens), SUM(cached_tokens),
				 SUM(conversation_count), SUM(llm_call_count)
				 FROM daily_token_usage WHERE sender_id = ? AND date >= ?
				 GROUP BY date ORDER BY date DESC`
		args = []any{senderID, since}
	} else {
		query = `SELECT date, sender_id, '' as model,
				 SUM(input_tokens), SUM(output_tokens), SUM(cached_tokens),
				 SUM(conversation_count), SUM(llm_call_count)
				 FROM daily_token_usage WHERE sender_id = ?
				 GROUP BY date ORDER BY date DESC`
		args = []any{senderID}
	}
	rows, err := conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get daily usage summary: %w", err)
	}
	defer rows.Close()

	var result []DailyTokenUsage
	for rows.Next() {
		var d DailyTokenUsage
		if err := rows.Scan(&d.Date, &d.SenderID, &d.Model, &d.InputTokens, &d.OutputTokens, &d.CachedTokens, &d.ConversationCount, &d.LLMCallCount); err != nil {
			return nil, fmt.Errorf("scan daily usage summary: %w", err)
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
