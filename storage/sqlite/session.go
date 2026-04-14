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

	displayOnly := 0
	if msg.DisplayOnly {
		displayOnly = 1
	}

	_, err := conn.Exec(`
			INSERT INTO session_messages
			(tenant_id, role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, display_only, reasoning_content, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
		tenantID, msg.Role, msg.Content,
		msg.ToolCallID, msg.ToolName, msg.ToolArguments,
		toolCallsJSON, msg.Detail, displayOnly, msg.ReasoningContent,
		ts.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert session message: %w", err)
	}
	return nil
}

// ReplaceToolMessage updates the most recent matching tool-role message.
//
// Parameters:
//   - toolName:    filter by tool_name. Empty string = match any (wildcard).
//   - toolCallID:  filter by tool_call_id. Empty string = match any (wildcard).
//   - content:     new content to write.
//
// Returns sql.ErrNoRows if no matching message exists.
func (s *SessionService) ReplaceToolMessage(tenantID int64, toolName, toolCallID, content string) error {
	conn := s.db.Conn()
	res, err := conn.Exec(`
		UPDATE session_messages SET content = ?
		WHERE id = (
			SELECT id FROM session_messages
			WHERE tenant_id = ? AND role = 'tool'
			  AND (? = '' OR tool_name = ?)
			  AND (? = '' OR tool_call_id = ?)
			ORDER BY id DESC LIMIT 1
		)
	`, content, tenantID, toolName, toolName, toolCallID, toolCallID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetHistory retrieves the most recent messages for a tenant.
// limit specifies the minimum number of user/assistant messages to return.
// Tool messages between them are included to maintain context continuity.
// display_only messages (e.g. cron results) are excluded from LLM context.
func (s *SessionService) GetHistory(tenantID int64, limit int) ([]llm.ChatMessage, error) {
	conn := s.db.Conn()

	// Find the boundary: the Nth user message from the end (0-indexed offset = limit - 1).
	// This way the window is measured in user-message turns, not raw row count,
	// so multi-iteration assistant messages don't squeeze out real conversation history.
	// Exclude display_only messages from boundary calculation.
	var boundaryID sql.NullInt64
	err := conn.QueryRow(`
		SELECT id FROM session_messages
		WHERE tenant_id = ? AND role = 'user' AND COALESCE(display_only, 0) = 0
		ORDER BY id DESC
		LIMIT 1 OFFSET ?
	`, tenantID, limit-1).Scan(&boundaryID)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("query history boundary: %w", err)
	}

	var rows *sql.Rows
	if boundaryID.Valid {
		rows, err = conn.Query(`
			SELECT role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, reasoning_content, created_at
				FROM session_messages
				WHERE tenant_id = ? AND id >= ? AND COALESCE(display_only, 0) = 0
				ORDER BY id ASC
			`, tenantID, boundaryID.Int64)
	} else {
		rows, err = conn.Query(`
				SELECT role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, reasoning_content, created_at
				FROM session_messages
				WHERE tenant_id = ? AND COALESCE(display_only, 0) = 0
				ORDER BY id ASC
			`, tenantID)
	}
	if err != nil {
		return nil, fmt.Errorf("query session history: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

// GetAllMessages retrieves all non-display-only messages for a tenant.
// Used by memory consolidation and context building.
//
// Design decision: display_only messages (e.g. cron task results) are intentionally
// excluded because they are produced by an independent agent loop with no shared
// conversation context. Including them in consolidation would inject unrelated content
// into the user's long-term memory summary. If future features need to retrieve cron
// execution history, a dedicated query (without the display_only filter) should be added.
func (s *SessionService) GetAllMessages(tenantID int64) ([]llm.ChatMessage, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
		SELECT role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, reasoning_content, created_at
		FROM session_messages
		WHERE tenant_id = ? AND COALESCE(display_only, 0) = 0
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

// GetUserMessageCount returns the number of user-role messages for a tenant.
// Used by consolidation logic to count conversation turns, not raw message rows
// (which include tool calls, assistant iterations, etc.).
// Excludes display_only messages (cron results).
func (s *SessionService) GetUserMessageCount(tenantID int64) (int, error) {
	conn := s.db.Conn()
	var count int
	err := conn.QueryRow(
		"SELECT COUNT(*) FROM session_messages WHERE tenant_id = ? AND role = 'user' AND COALESCE(display_only, 0) = 0",
		tenantID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count user messages: %w", err)
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

// PurgeNewerThanOrEqual deletes all messages for a tenant with created_at >= the given timestamp.
// Used by Ctrl+K rewind to truncate DB history to match UI truncation.
// Uses ">=" (not ">") so the selected rewind message is also removed — the UI already
// places its content into the input box for re-editing, so keeping it in DB would cause
// a duplicate on re-send.
func (s *SessionService) PurgeNewerThanOrEqual(tenantID int64, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, nil
	}
	conn := s.db.Conn()
	// IMPORTANT: created_at is stored as RFC3339 TEXT (e.g. "2026-04-14T20:34:25+08:00").
	// We must compare against the same string format — passing time.Time directly causes
	// modernc.org/sqlite to serialize it differently (e.g. "2026-04-14 20:34:25+08:00"),
	// which breaks lexicographic comparison and deletes ALL messages.
	cutoffStr := cutoff.Format(time.RFC3339)
	result, err := conn.Exec(
		"DELETE FROM session_messages WHERE tenant_id = ? AND created_at >= ?",
		tenantID, cutoffStr,
	)
	if err != nil {
		return 0, fmt.Errorf("purge newer or equal: %w", err)
	}
	rows, _ := result.RowsAffected()
	log.WithFields(log.Fields{
		"tenant_id": tenantID,
		"purged":    rows,
		"cutoff":    cutoff.Format(time.RFC3339),
	}).Info("Session messages purged (newer or equal)")
	return rows, nil
}

// PurgeNewerThan deletes all messages for a tenant with created_at after the given timestamp.
// Used by Ctrl+K rewind to truncate DB history to match UI truncation.
// NOTE: Prefer PurgeNewerThanOrEqual for rewind to avoid duplicate user messages on re-send.
func (s *SessionService) PurgeNewerThan(tenantID int64, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, nil
	}
	conn := s.db.Conn()
	// IMPORTANT: created_at is stored as RFC3339 TEXT (e.g. "2026-04-14T20:34:25+08:00").
	// We must compare against the same string format — passing time.Time directly causes
	// modernc.org/sqlite to serialize it differently (e.g. "2026-04-14 20:34:25+08:00"),
	// which breaks lexicographic comparison and deletes ALL messages.
	cutoffStr := cutoff.Format(time.RFC3339)
	result, err := conn.Exec(
		"DELETE FROM session_messages WHERE tenant_id = ? AND created_at > ?",
		tenantID, cutoffStr,
	)
	if err != nil {
		return 0, fmt.Errorf("purge newer than: %w", err)
	}
	rows, _ := result.RowsAffected()
	log.WithFields(log.Fields{
		"tenant_id": tenantID,
		"purged":    rows,
		"cutoff":    cutoff.Format(time.RFC3339),
	}).Info("Session messages purged (newer than)")
	return rows, nil
}

// UpdateMessageContent updates the content of the Nth message (0-indexed) for a tenant.
// Used by observation masking to persist masked content back to session.
func (s *SessionService) UpdateMessageContent(tenantID int64, messageIndex int, content string) error {
	conn := s.db.Conn()
	result, err := conn.Exec(`
		UPDATE session_messages SET content = ?
		WHERE tenant_id = ? AND id = (
			SELECT id FROM session_messages
			WHERE tenant_id = ?
			ORDER BY id ASC
			LIMIT 1
			OFFSET ?
		)
	`, content, tenantID, tenantID, messageIndex)
	if err != nil {
		return fmt.Errorf("update message content at index %d: %w", messageIndex, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no message found at index %d for tenant %d", messageIndex, tenantID)
	}
	return nil
}

// scanMessages scans message rows from a query result
func (s *SessionService) scanMessages(rows *sql.Rows) ([]llm.ChatMessage, error) {
	var messages []llm.ChatMessage
	for rows.Next() {
		var msg llm.ChatMessage
		var toolCallsJSON, detailJSON sql.NullString
		var toolCallID, toolName, toolArguments, reasoningContent sql.NullString
		var createdAt string

		err := rows.Scan(
			&msg.Role, &msg.Content,
			&toolCallID, &toolName, &toolArguments,
			&toolCallsJSON, &detailJSON, &reasoningContent, &createdAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}

		if toolCallID.Valid {
			msg.ToolCallID = toolCallID.String
		}
		if toolName.Valid {
			msg.ToolName = toolName.String
		}
		if toolArguments.Valid {
			msg.ToolArguments = toolArguments.String
		}
		if detailJSON.Valid {
			msg.Detail = detailJSON.String
		}
		if toolCallsJSON.Valid {
			if err := json.Unmarshal([]byte(toolCallsJSON.String), &msg.ToolCalls); err != nil {
				log.WithError(err).Warn("Failed to unmarshal tool_calls, skipping")
			}
		}
		if reasoningContent.Valid {
			msg.ReasoningContent = reasoningContent.String
		}

		msg.Timestamp = internal.ParseTimestamp(createdAt)

		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return messages, nil
}
