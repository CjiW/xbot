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

	// Find the boundary: the Nth user message from the end (0-indexed offset = limit - 1).
	// This way the window is measured in user-message turns, not raw row count,
	// so multi-iteration assistant messages don't squeeze out real conversation history.
	var boundaryID sql.NullInt64
	err := conn.QueryRow(`
		SELECT id FROM session_messages
		WHERE tenant_id = ? AND role = 'user'
		ORDER BY id DESC
		LIMIT 1 OFFSET ?
	`, tenantID, limit-1).Scan(&boundaryID)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("query history boundary: %w", err)
	}

	var rows *sql.Rows
	if boundaryID.Valid {
		// Fetch all messages from the boundary user message onwards
		rows, err = conn.Query(`
			SELECT role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, created_at
			FROM session_messages
			WHERE tenant_id = ? AND id >= ?
			ORDER BY id ASC
		`, tenantID, boundaryID.Int64)
	} else {
		// Fewer than `limit` user messages exist, return all
		rows, err = conn.Query(`
			SELECT role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, created_at
			FROM session_messages
			WHERE tenant_id = ?
			ORDER BY id ASC
		`, tenantID)
	}
	if err != nil {
		return nil, fmt.Errorf("query session history: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
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

// PurgeOldMessages deletes messages older than the most recent `keepCount` messages for a tenant.
// This is used after compression to remove messages that have already been summarized.
func (s *SessionService) PurgeOldMessages(tenantID int64, keepCount int) (int64, error) {
	if keepCount <= 0 {
		return 0, nil
	}
	conn := s.db.Conn()

	// Find the ID of the message at position `keepCount` from the end (i.e., the oldest message to keep).
	// Messages with ID < cutoff will be deleted.
	var cutoffID sql.NullInt64
	err := conn.QueryRow(`
		SELECT id FROM session_messages
		WHERE tenant_id = ?
		ORDER BY id DESC
		LIMIT 1
		OFFSET ?
	`, tenantID, keepCount).Scan(&cutoffID)
	if err == sql.ErrNoRows {
		// Fewer messages than keepCount, nothing to purge
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find purge cutoff: %w", err)
	}

	if !cutoffID.Valid {
		return 0, nil
	}

	result, err := conn.Exec("DELETE FROM session_messages WHERE tenant_id = ? AND id < ?", tenantID, cutoffID.Int64)
	if err != nil {
		return 0, fmt.Errorf("purge old messages: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows > 0 {
		log.WithFields(log.Fields{
			"tenant_id": tenantID,
			"purged":    rows,
			"kept":      keepCount,
			"cutoff_id": cutoffID.Int64,
		}).Info("Purged old messages after compression")
	}
	return rows, nil
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
