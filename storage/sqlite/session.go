package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"xbot/llm"
	log "xbot/logger"
	"xbot/storage/internal"
)

// SessionService handles session message operations
type SessionService struct {
	db *DB
}

// NewSessionService creates a new session service
func NewSessionService(db *DB) *SessionService {
	return &SessionService{db: db}
}

// AddMessage adds a message to a tenant's session
func (s *SessionService) AddMessage(tenantID int64, msg llm.ChatMessage) error {
	conn := s.db.Conn()

	var toolCallsJSON sql.NullString
	if len(msg.ToolCalls) > 0 {
		data, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			return fmt.Errorf("marshal tool_calls: %w", err)
		}
		toolCallsJSON = sql.NullString{String: string(data), Valid: true}
	}

	ts := msg.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	_, err := conn.Exec(`
		INSERT INTO session_messages
		(tenant_id, role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		tenantID, msg.Role, msg.Content,
		msg.ToolCallID, msg.ToolName, msg.ToolArguments,
		toolCallsJSON, msg.Detail,
		ts.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert session message: %w", err)
	}
	return nil
}

// GetHistory retrieves the most recent messages for a tenant.
// limit specifies the minimum number of user/assistant messages to return.
// Tool messages between them are included to maintain context continuity.
func (s *SessionService) GetHistory(tenantID int64, limit int) ([]llm.ChatMessage, error) {
	conn := s.db.Conn()

	// Use a generous upper bound to ensure we get at least `limit` non-tool messages.
	// Tool messages are interspersed between user/assistant messages and count toward
	// the total, so we fetch limit * 3 to have enough headroom.
	upperBound := limit * 3
	if upperBound < 100 {
		upperBound = 100
	}

	rows, err := conn.Query(`
		SELECT role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, created_at
		FROM session_messages
		WHERE tenant_id = ?
		ORDER BY id DESC
		LIMIT ?
	`, tenantID, upperBound)
	if err != nil {
		return nil, fmt.Errorf("query session history: %w", err)
	}
	defer rows.Close()

	messages, err := s.scanMessages(rows)
	if err != nil {
		return nil, err
	}

	// Reverse to chronological order (oldest first)
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	// Trim from the beginning to keep at most `limit` user/assistant messages.
	// Walk from the end to find the cut point where we have exactly `limit`
	// non-tool messages in the tail.
	nonToolCount := 0
	cutIdx := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "tool" {
			nonToolCount++
			if nonToolCount == limit {
				cutIdx = i
				break
			}
		}
	}
	if cutIdx > 0 {
		messages = messages[cutIdx:]
	}

	return messages, nil
}

// GetAllMessages retrieves all messages for a tenant
func (s *SessionService) GetAllMessages(tenantID int64) ([]llm.ChatMessage, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
		SELECT role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, created_at
		FROM session_messages
		WHERE tenant_id = ?
		ORDER BY id ASC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query all session messages: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

// GetMessagesCount returns the number of messages for a tenant
func (s *SessionService) GetMessagesCount(tenantID int64) (int, error) {
	conn := s.db.Conn()
	var count int
	err := conn.QueryRow(
		"SELECT COUNT(*) FROM session_messages WHERE tenant_id = ?",
		tenantID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count messages: %w", err)
	}
	return count, nil
}

// Clear removes all messages for a tenant
func (s *SessionService) Clear(tenantID int64) error {
	conn := s.db.Conn()
	result, err := conn.Exec("DELETE FROM session_messages WHERE tenant_id = ?", tenantID)
	if err != nil {
		return fmt.Errorf("clear session messages: %w", err)
	}
	rows, _ := result.RowsAffected()
	log.WithFields(log.Fields{
		"tenant_id": tenantID,
		"messages":  rows,
	}).Debug("Session messages cleared")
	return nil
}

// scanMessages scans message rows from a query result
func (s *SessionService) scanMessages(rows *sql.Rows) ([]llm.ChatMessage, error) {
	var messages []llm.ChatMessage
	for rows.Next() {
		var msg llm.ChatMessage
		var toolCallsJSON sql.NullString
		var createdAt string

		err := rows.Scan(
			&msg.Role, &msg.Content,
			&msg.ToolCallID, &msg.ToolName, &msg.ToolArguments,
			&toolCallsJSON, &msg.Detail, &createdAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}

		if toolCallsJSON.Valid {
			if err := json.Unmarshal([]byte(toolCallsJSON.String), &msg.ToolCalls); err != nil {
				log.WithError(err).Warn("Failed to unmarshal tool_calls, skipping")
			}
		}

		msg.Timestamp = internal.ParseTimestamp(createdAt)

		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return messages, nil
}
